package api

import (
	"net/http"

	"github.com/opencode-ai/opencode/internal/skill"
)

// APISkill is the SDK-facing skill representation. Matches the dax
// `/skill` response shape (name, description, location, content).
type APISkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location"`
	Content     string `json:"content"`
}

// handleSkillList returns all discovered skills (project + global + custom paths).
func (s *Server) handleSkillList(w http.ResponseWriter, r *http.Request) {
	skills := skill.All()
	out := make([]APISkill, 0, len(skills))
	for _, sk := range skills {
		out = append(out, APISkill{
			Name:        sk.Name,
			Description: sk.Description,
			Location:    sk.Location,
			Content:     sk.Content,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
