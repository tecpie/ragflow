package main

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/websocket"
)

type websocketProxy struct {
	upstreamOrigin string
	upgrader       websocket.Upgrader
	dialer         websocket.Dialer
}

func newWebSocketProxy(upstream string) *websocketProxy {
	return &websocketProxy{
		upstreamOrigin: strings.Replace(strings.Replace(upstream, "https://", "wss://", 1), "http://", "ws://", 1),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		},
		dialer: websocket.Dialer{
			HandshakeTimeout: 0,
		},
	}
}

func (p *websocketProxy) serve(w http.ResponseWriter, r *http.Request) {
	clientConn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("websocket upgrade failed: %v", err)
		return
	}
	defer clientConn.Close()

	target, err := url.Parse(p.upstreamOrigin)
	if err != nil {
		log.Printf("invalid upstream websocket origin: %v", err)
		return
	}
	target.Path = r.URL.Path
	target.RawQuery = r.URL.RawQuery

	upstreamConn, _, err := p.dialer.Dial(target.String(), nil)
	if err != nil {
		log.Printf("connect upstream websocket failed: %v", err)
		_ = clientConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "upstream unavailable"))
		return
	}
	defer upstreamConn.Close()

	errCh := make(chan error, 2)
	go func() { errCh <- copyWebSocket(upstreamConn, clientConn) }()
	go func() { errCh <- copyWebSocket(clientConn, upstreamConn) }()
	<-errCh
}

func copyWebSocket(dst, src *websocket.Conn) error {
	for {
		messageType, reader, err := src.NextReader()
		if err != nil {
			return err
		}
		writer, err := dst.NextWriter(messageType)
		if err != nil {
			return err
		}
		if _, err := io.Copy(writer, reader); err != nil {
			_ = writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
	}
}
