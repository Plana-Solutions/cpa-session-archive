package main

import (
	"encoding/json"
	"flag"
	"gopkg.in/yaml.v3"
	"log"
	"net/http"
	"os"
	"strings"

	"cpa-session-archive/internal/archive"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:4274", "browser listener")
	clickhouse := flag.String("clickhouse", os.Getenv("CLICKHOUSE_URL"), "ClickHouse reader HTTP URL")
	flag.Parse()
	p := archive.NewPlugin()
	settings, _ := yaml.Marshal(map[string]string{"clickhouse-url": *clickhouse, "clickhouse-user": "inference_reader", "clickhouse-password": os.Getenv("CLICKHOUSE_PASSWORD")})
	config, _ := json.Marshal(map[string][]byte{"config_yaml": settings})
	if _, err := p.Handle("plugin.register", config); err != nil {
		log.Fatal(err)
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) })
	http.HandleFunc("/v0/management/api-key-aliases", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"items":[]}`)) })
	http.HandleFunc("/v0/management/plugins/cpa-key-policy/identities", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"identities":[]}`)) })

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
		result, err := p.HandleContext(r.Context(), "management.handle", request)
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
