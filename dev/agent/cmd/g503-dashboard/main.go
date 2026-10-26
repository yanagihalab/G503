package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type dashboardData struct {
	APIURL    string
	UpdatedAt string
	Agents    string
	Params    string
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8090", "dashboard listen address")
	apiURL := flag.String("api-url", "http://127.0.0.1:1317", "G503 REST API URL")
	flag.Parse()

	client := &http.Client{Timeout: 5 * time.Second}
	tpl := template.Must(template.New("dashboard").Parse(pageHTML))
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		agents := fetchJSON(client, strings.TrimRight(*apiURL, "/")+"/sanction/v1/agents")
		params := fetchJSON(client, strings.TrimRight(*apiURL, "/")+"/sanction/v1/params")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = tpl.Execute(w, dashboardData{
			APIURL:    strings.TrimRight(*apiURL, "/"),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Agents:    agents,
			Params:    params,
		})
	})
	log.Printf("g503 dashboard listening on http://%s", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatal(err)
	}
}

func fetchJSON(client *http.Client, url string) string {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	defer resp.Body.Close()
	bz, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	var value any
	if err := json.Unmarshal(bz, &value); err != nil {
		return string(bz)
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return string(bz)
	}
	return string(pretty)
}

const pageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>G503 Operations Dashboard</title>
  <style>
    body { font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 0; background: #f7f7f4; color: #202124; }
    header { padding: 18px 24px; background: #173b33; color: white; }
    main { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 16px; padding: 18px; }
    section { background: white; border: 1px solid #d8d8d2; border-radius: 8px; overflow: hidden; }
    h1 { font-size: 20px; margin: 0 0 4px; }
    h2 { font-size: 15px; margin: 0; padding: 12px 14px; border-bottom: 1px solid #e4e4df; background: #fbfbf8; }
    p { margin: 0; font-size: 13px; color: #dcece8; }
    pre { margin: 0; padding: 12px 14px; white-space: pre-wrap; word-break: break-word; font-size: 12px; line-height: 1.45; }
    a { color: #c6efe3; }
  </style>
</head>
<body>
  <header>
    <h1>G503 Operations Dashboard</h1>
    <p>API: <a href="{{.APIURL}}">{{.APIURL}}</a> · updated {{.UpdatedAt}}</p>
  </header>
  <main>
    <section>
      <h2>Sanction Agents</h2>
      <pre>{{.Agents}}</pre>
    </section>
    <section>
      <h2>Sanction Params</h2>
      <pre>{{.Params}}</pre>
    </section>
  </main>
</body>
</html>`
