// muster calls the roll of container images on every Kubernetes node:
// it reconciles a MANIFEST (a plain text file, one image reference per
// line) against each node's reported image cache, and exports the
// discrepancies as Prometheus metrics.
//
// Two directions, like any honest inventory:
//
//   - muster_absent{node,image}: an image the manifest expects that a
//     node's cache does not hold -- if that image's registry is
//     unreachable when the node next needs it, the workload will not
//     start.
//   - muster_unlisted{namespace,image}: an image RUNNING in a watched
//     namespace that the manifest does not list -- the ledger has
//     drifted from reality (typically after a cluster upgrade).
//
// Manifest entries pinned with @sha256 match by digest; bare
// repo:tag entries match by exact name.  The manifest is re-read on
// every reconcile pass, so a ConfigMap-mounted file picks up changes
// without a restart.
//
// muster reads ONLY the Kubernetes API (nodes; pods when drift
// namespaces are configured) with its ServiceAccount -- no node
// agents, no container-runtime sockets, no privileges.  The price of
// that choice: node image lists are whatever the kubelet reports,
// and kubelet TRUNCATES them to nodeStatusMaxImages entries (default
// 50, largest first) -- small images fall off the report while still
// cached.  Raise nodeStatusMaxImages or expect false absences; see
// the README.
//
// Standard library only, by policy.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	tokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	caPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

func main() {
	fs := flag.NewFlagSet("muster", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "image manifest file: one image per line, # comments (required)")
	listen := fs.String("listen", ":9909", "metrics listener address")
	interval := fs.Duration("interval", time.Minute, "reconcile interval")
	driftNS := fs.String("drift-namespaces", "", "comma-separated namespaces whose RUNNING pod images must appear in the manifest (empty disables drift detection)")
	version := fs.Bool("version", false, "print the program name and exit")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2) // flag already printed the message and usage
	}
	if *version {
		fmt.Println("muster")
		os.Exit(0)
	}
	if *manifest == "" {
		fmt.Fprintln(os.Stderr, "muster: -manifest is required")
		fs.Usage()
		os.Exit(2)
	}

	var namespaces []string
	for _, ns := range strings.Split(*driftNS, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			namespaces = append(namespaces, ns)
		}
	}

	c, err := inCluster()
	if err != nil {
		fmt.Fprintln(os.Stderr, "muster:", err)
		os.Exit(1)
	}

	var (
		mu    sync.Mutex
		state reconcileState
	)
	run := func() {
		s := reconcile(c, *manifest, namespaces)
		mu.Lock()
		state = s
		mu.Unlock()
	}
	run()
	go func() {
		t := time.NewTicker(*interval)
		for range t.C {
			run()
		}
	}()

	http.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		s := state
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write(renderMetrics(s))
	})
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "muster -- see /metrics")
	})
	fmt.Printf("muster: manifest %s, every %s, drift namespaces %q, listening %s\n",
		*manifest, *interval, namespaces, *listen)
	err = http.ListenAndServe(*listen, nil)
	fmt.Fprintln(os.Stderr, "muster:", err)
	os.Exit(1)
}

// client is the minimal API access muster needs: an authenticated GET.
type client interface {
	get(path string, into any) error
}

type kubeClient struct {
	base string
	http *http.Client
}

func inCluster() (client, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("not in a cluster: KUBERNETES_SERVICE_HOST/PORT unset")
	}
	ca, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("reading cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("no certificates parsed from %s", caPath)
	}
	return &kubeClient{
		base: "https://" + host + ":" + port,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		},
	}, nil
}

func (k *kubeClient) get(path string, into any) error {
	// Re-read the token every request: ServiceAccount tokens rotate.
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("reading token: %w", err)
	}
	req, err := http.NewRequest("GET", k.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// The API shapes muster reads, cut to the fields it uses.
type nodeList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Images []struct {
				Names []string `json:"names"`
			} `json:"images"`
		} `json:"status"`
	} `json:"items"`
}

type podList struct {
	Items []struct {
		Spec struct {
			InitContainers []struct {
				Image string `json:"image"`
			} `json:"initContainers"`
			Containers []struct {
				Image string `json:"image"`
			} `json:"containers"`
		} `json:"spec"`
	} `json:"items"`
}

type reconcileState struct {
	when     time.Time
	ok       bool
	entries  int
	absent   map[string][]string // node -> manifest entries not in its cache
	reported map[string]int      // node -> image entries the kubelet reported
	unlisted map[string][]string // namespace -> running images missing from the manifest
}

func reconcile(c client, manifestPath string, driftNamespaces []string) reconcileState {
	s := reconcileState{
		when:     time.Now(),
		absent:   map[string][]string{},
		reported: map[string]int{},
		unlisted: map[string][]string{},
	}
	entries, err := readManifest(manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "muster:", err)
		return s
	}
	s.entries = len(entries)

	var nodes nodeList
	if err := c.get("/api/v1/nodes", &nodes); err != nil {
		fmt.Fprintln(os.Stderr, "muster:", err)
		return s
	}
	for _, n := range nodes.Items {
		var names []string
		for _, img := range n.Status.Images {
			names = append(names, img.Names...)
		}
		s.reported[n.Metadata.Name] = len(n.Status.Images)
		for _, e := range entries {
			if !cacheHolds(names, e) {
				s.absent[n.Metadata.Name] = append(s.absent[n.Metadata.Name], e)
			}
		}
		sort.Strings(s.absent[n.Metadata.Name])
	}

	for _, ns := range driftNamespaces {
		var pods podList
		if err := c.get("/api/v1/namespaces/"+ns+"/pods?fieldSelector=status.phase%3DRunning", &pods); err != nil {
			fmt.Fprintln(os.Stderr, "muster:", err)
			return s
		}
		seen := map[string]bool{}
		for _, p := range pods.Items {
			var images []string
			for _, ct := range p.Spec.InitContainers {
				images = append(images, ct.Image)
			}
			for _, ct := range p.Spec.Containers {
				images = append(images, ct.Image)
			}
			for _, img := range images {
				if !manifestLists(entries, img) && !seen[img] {
					seen[img] = true
					s.unlisted[ns] = append(s.unlisted[ns], img)
				}
			}
		}
		sort.Strings(s.unlisted[ns])
	}
	s.ok = true
	return s
}

func readManifest(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	sort.Strings(out)
	return out, nil
}

// digestOf returns the sha256:... suffix of a pinned reference, or "".
func digestOf(ref string) string {
	if i := strings.Index(ref, "@sha256:"); i >= 0 {
		return ref[i+1:]
	}
	return ""
}

// cacheHolds reports whether a node's reported image names satisfy a
// manifest entry: digest-pinned entries match any name carrying the
// same digest; bare entries need an exact name match.
func cacheHolds(names []string, entry string) bool {
	if d := digestOf(entry); d != "" {
		for _, n := range names {
			if strings.Contains(n, d) {
				return true
			}
		}
		return false
	}
	for _, n := range names {
		if n == entry {
			return true
		}
	}
	return false
}

// manifestLists reports whether a running image is covered by the
// manifest: digest matches digest; a bare running reference is also
// covered by a manifest entry that pins the same repo:tag.
func manifestLists(entries []string, img string) bool {
	d := digestOf(img)
	for _, e := range entries {
		if e == img {
			return true
		}
		if d != "" && digestOf(e) == d {
			return true
		}
		if d == "" && strings.HasPrefix(e, img+"@") {
			return true
		}
	}
	return false
}

func renderMetrics(s reconcileState) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP muster_reconcile_success whether the last reconcile pass completed\n# TYPE muster_reconcile_success gauge\nmuster_reconcile_success %d\n", boolTo01(s.ok))
	fmt.Fprintf(&b, "# HELP muster_reconcile_timestamp_seconds when the last reconcile pass ran\n# TYPE muster_reconcile_timestamp_seconds gauge\nmuster_reconcile_timestamp_seconds %d\n", s.when.Unix())
	fmt.Fprintf(&b, "# HELP muster_manifest_entries images the manifest expects\n# TYPE muster_manifest_entries gauge\nmuster_manifest_entries %d\n", s.entries)

	b.WriteString("# HELP muster_node_images_reported image entries the kubelet reported for the node (kubelet truncates at nodeStatusMaxImages)\n# TYPE muster_node_images_reported gauge\n")
	for _, node := range sortedKeys(s.reported) {
		fmt.Fprintf(&b, "muster_node_images_reported{node=%q} %d\n", node, s.reported[node])
	}

	b.WriteString("# HELP muster_absent_count manifest images missing from the node's reported cache\n# TYPE muster_absent_count gauge\n")
	for _, node := range sortedKeys(s.reported) {
		fmt.Fprintf(&b, "muster_absent_count{node=%q} %d\n", node, len(s.absent[node]))
	}
	b.WriteString("# HELP muster_absent a manifest image missing from the node's reported cache\n# TYPE muster_absent gauge\n")
	for _, node := range sortedKeys(s.absent) {
		for _, img := range s.absent[node] {
			fmt.Fprintf(&b, "muster_absent{node=%q,image=%q} 1\n", node, img)
		}
	}

	b.WriteString("# HELP muster_unlisted_count running images the manifest does not list, per watched namespace\n# TYPE muster_unlisted_count gauge\n")
	for _, ns := range sortedKeys(s.unlisted) {
		fmt.Fprintf(&b, "muster_unlisted_count{namespace=%q} %d\n", ns, len(s.unlisted[ns]))
	}
	b.WriteString("# HELP muster_unlisted a running image the manifest does not list\n# TYPE muster_unlisted gauge\n")
	for _, ns := range sortedKeys(s.unlisted) {
		for _, img := range s.unlisted[ns] {
			fmt.Fprintf(&b, "muster_unlisted{namespace=%q,image=%q} 1\n", ns, img)
		}
	}
	return []byte(b.String())
}

func boolTo01(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
