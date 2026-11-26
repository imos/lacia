package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/imos/lacia/internal/discord"
)

// AttachmentSender is present only on the Discord server.
type AttachmentSender interface {
	SendPDF(context.Context, string, string, string, []byte) (discord.AttachmentResult, error)
}

func (s *Server) apiDiscordAttachment(w http.ResponseWriter, r *http.Request) {
	if s.Attachments == nil || s.Requests == nil || s.Store == nil {
		http.NotFound(w, r)
		return
	}
	var in struct {
		RequestID string `json:"request_id"`
		Path      string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "添付ファイルの指定を読み取れませんでした"})
		return
	}
	original, ok := s.Requests.Get(in.RequestID)
	channel, routed := strings.CutPrefix(original.ReplyTo, "discord:")
	if !ok || !routed || channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Discordから届いた元の依頼のIDを指定してください"})
		return
	}
	name, data, err := discord.ReadPDF(s.Store.Root(), in.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := s.Attachments.SendPDF(r.Context(), channel, original.ID, name, data)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "attachment": result})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
