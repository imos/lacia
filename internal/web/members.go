package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/imos/lacia/internal/discord"
)

// MemberControl lets the conversation see who the members on
// Discord are, and pause a child's messages when it judges a break is due.
type MemberControl interface {
	Members() []discord.MemberStatus
	Pause(who string, d time.Duration) (discord.Member, time.Time, error)
	Resume(who string) (discord.Member, error)
}

func (s *Server) apiMembers(w http.ResponseWriter, r *http.Request) {
	if s.Members == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Discordの設定は、このサーバーにはありません"})
		return
	}
	writeJSON(w, http.StatusOK, s.Members.Members())
}

// apiMemberPause is for Lacia herself, through `lacia discord pause` and
// `lacia discord resume`.
func (s *Server) apiMemberPause(w http.ResponseWriter, r *http.Request) {
	if s.Members == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Discordの設定は、このサーバーにはありません"})
		return
	}
	var in struct {
		Who     string `json:"who"`
		Minutes int    `json:"minutes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": invalidJSONMessage})
		return
	}
	if action := r.PathValue("action"); action != "pause" && action != "resume" {
		http.NotFound(w, r)
		return
	}
	if r.PathValue("action") == "resume" {
		m, err := s.Members.Resume(in.Who)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, discord.MemberStatus{Member: m})
		return
	}
	m, until, err := s.Members.Pause(in.Who, time.Duration(in.Minutes)*time.Minute)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, discord.MemberStatus{Member: m, PausedUntil: &until})
}
