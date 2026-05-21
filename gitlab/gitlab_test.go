package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupMockGitLabServer() (*httptest.Server, func()) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("PRIVATE-TOKEN")
		if token != "valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/v4/groups/test-namespace/projects", func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("PRIVATE-TOKEN")
		if token != "valid-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		projects := []map[string]interface{}{
			{"id": 1, "name": "project1"},
			{"id": 2, "name": "project2"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(projects)
	})

	server := httptest.NewServer(mux)
	return server, server.Close
}

func TestInit(t *testing.T) {
	server, cleanup := setupMockGitLabServer()
	defer cleanup()

	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{
			name:    "valid token",
			token:   "valid-token",
			wantErr: false,
		},
		{
			name:    "empty token",
			token:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GitlabInfo{
				Token:   tt.token,
				BaseURL: server.URL + "/api/v4",
			}
			err := g.Init(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if g.client == nil {
					t.Error("expected client to be set but got nil")
				}
			}
		})
	}
}

func TestListProject(t *testing.T) {
	server, cleanup := setupMockGitLabServer()
	defer cleanup()

	tests := []struct {
		name      string
		token     string
		namespace string
		wantErr   bool
		wantCount int
	}{
		{
			name:      "valid credentials",
			token:     "valid-token",
			namespace: "test-namespace",
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:      "invalid token",
			token:     "invalid-token",
			namespace: "test-namespace",
			wantErr:   true,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GitlabInfo{
				Token:    tt.token,
				GitlabNs: tt.namespace,
				BaseURL:  server.URL + "/api/v4",
			}
			if err := g.Init(context.Background()); err != nil {
				if tt.wantErr {
					return
				}
				t.Fatalf("unexpected init error: %v", err)
			}
			projects, err := g.ListProject(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				if len(projects) != 0 {
					t.Errorf("expected 0 projects but got %d", len(projects))
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if len(projects) != tt.wantCount {
					t.Errorf("expected %d projects but got %d", tt.wantCount, len(projects))
				}
				for _, project := range projects {
					if project.ProjectName == "" {
						t.Error("project name should not be empty")
					}
					if project.ProjectId == "" {
						t.Error("project ID should not be empty")
					}
				}
			}
		})
	}
}

func ListVariables(t *testing.T) {}

func CreateVariable(t *testing.T) {}

func UpdateVariable(t *testing.T) {}
