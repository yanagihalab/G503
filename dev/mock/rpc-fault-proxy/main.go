package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yanagihalab/G503/app"
	"github.com/yanagihalab/G503/x/sanction/keeper"
	st "github.com/yanagihalab/G503/x/sanction/types"
)

// This loopback-only test proxy withholds one successful execute acknowledgement.
func main() {
	listen := flag.String("listen", "127.0.0.1:59205", "test proxy listen address")
	upstream := flag.String("upstream", "http://127.0.0.1:59357", "upstream CometBFT RPC")
	marker := flag.String("marker", "", "marker written after forwarding execute tx")
	messageType := flag.String("message-type", "execute", "message whose acknowledgement is withheld: execute or open")
	flag.Parse()
	if *marker == "" {
		log.Fatal("marker is required")
	}
	if *messageType != "execute" && *messageType != "open" {
		log.Fatal("unsupported message-type")
	}
	var dropped atomic.Bool
	if _, err := os.Stat(*marker); err == nil {
		dropped.Store(true)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var rpc struct {
			Method string `json:"method"`
			Params struct {
				Tx string `json:"tx"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &rpc)
		isExecute := false
		var txBytes []byte
		if rpc.Method == "broadcast_tx_sync" {
			txBytes, err = base64.StdEncoding.DecodeString(rpc.Params.Tx)
			if err == nil {
				tx, e := app.MakeEncodingConfig().TxConfig.TxDecoder()(txBytes)
				if e == nil {
					for _, msg := range tx.GetMsgs() {
						_, execute := msg.(*st.MsgExecuteSanction)
						_, open := msg.(*st.MsgOpenSanctionCase)
						if (*messageType == "execute" && execute) || (*messageType == "open" && open) {
							isExecute = true
						}
					}
				}
			}
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(*upstream, "/")+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		var result struct {
			Result *struct {
				Code *uint32 `json:"code"`
			} `json:"result"`
		}
		_ = json.Unmarshal(payload, &result)
		if isExecute && result.Result != nil && result.Result.Code != nil && *result.Result.Code == 0 && dropped.CompareAndSwap(false, true) {
			bz, _ := json.Marshal(map[string]any{"hash": strings.ToUpper(hex.EncodeToString(keeper.TxHash(txBytes))), "accepted": true})
			file, e := os.OpenFile(*marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				log.Print(e)
			} else {
				_, e = file.Write(bz)
				if e == nil {
					e = file.Sync()
				}
				_ = file.Close()
				if e != nil {
					log.Print(e)
				}
			}
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(payload)
	})
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	log.Fatal(server.ListenAndServe())
}
