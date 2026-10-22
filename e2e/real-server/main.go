package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"cpa-session-archive/internal/archive"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:4174", "browser listener")
	collector := flag.String("collector", "http://127.0.0.1:4184", "real collector URL")
	flag.Parse()
	p := archive.NewPlugin()
	config, _ := json.Marshal(map[string][]byte{"config_yaml": []byte("endpoint: " + *collector + "\n")})
	if _, err := p.Handle("plugin.register", config); err != nil {
		log.Fatal(err)
	}
	u, err := url.Parse(*collector)
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	http.Handle("/archive-api/", proxy)
	http.Handle("/healthz", proxy)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			http.Redirect(w, r, "/v0/resource/plugins/cpa-session-archive/index.html", http.StatusFound)
			return
		}
		if strings.HasPrefix(path, "/v0/management/") && r.Header.Get("Authorization") != "Bearer fixture-management-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		request, _ := json.Marshal(map[string]any{
			"Method": r.Method, "Path": path, "Headers": r.Header, "Query": r.URL.Query(),
		})
		result, err := p.Handle("management.handle", request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var envelope struct {
			Result struct {
				StatusCode int
				Headers    http.Header
				Body       []byte
			}
		}
		if err := json.Unmarshal(result, &envelope); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for name, values := range envelope.Result.Headers {
			w.Header()[name] = values
		}
		w.WriteHeader(envelope.Result.StatusCode)
		_, _ = w.Write(envelope.Result.Body)
	})
	log.Fatal(http.ListenAndServe(*listen, nil))
}
