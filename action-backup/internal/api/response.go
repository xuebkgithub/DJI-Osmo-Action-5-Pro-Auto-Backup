// Package api 提供 REST 路由与处理器。
package api

import (
	"encoding/json"
	"net/http"
)

type envelope struct {
	Data any `json:"data"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func writeData(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, envelope{Data: v}) }

func writeError(w http.ResponseWriter, status int, code, msg string, retryable bool) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: msg, Retryable: retryable}})
}
