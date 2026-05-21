package gitlab

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"text/template"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

type GitlabInfo struct {
	Token    string
	GitlabNs string
	BaseURL  string
	client   *GitlabClient
}

type GitlabVariable struct {
	Key   string
	Value string
}

type GitlabResp struct {
	ProjectName string
	ProjectId   string
}

type GitlabClient struct {
	*gitlab.Client
}

func (g *GitlabInfo) Init(ctx context.Context) error {
	if g.Token == "" {
		return errors.New("token cannot be empty")
	}

	baseURL := g.BaseURL
	if baseURL == "" {
		baseURL = "http://127.0.1:8080/api/v4"
	}

	client, err := gitlab.NewClient(g.Token, gitlab.WithBaseURL(baseURL))
	if err != nil {
		return err
	}

	g.client = &GitlabClient{Client: client}
	return nil
}

func (g *GitlabInfo) ListProject(ctx context.Context) ([]*GitlabResp, error) {
	ctx, span := otel.Tracer("gitlab").Start(ctx, "ListProject")
	defer span.End()

	projList, _, err := g.client.Groups.ListGroupProjects(g.GitlabNs, &gitlab.ListGroupProjectsOptions{
		Archived: gitlab.Ptr(false),
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	respList := make([]*GitlabResp, 0, len(projList))
	for _, repo := range projList {
		respList = append(respList, &GitlabResp{
			ProjectName: repo.Name,
			ProjectId:   strconv.Itoa(repo.ID),
		})
	}
	return respList, nil
}

func (g *GitlabInfo) AddGitlabCiFile(ctx context.Context, gr *GitlabResp, content string) error {
	ctx, span := otel.Tracer("gitlab").Start(ctx, "AddGitlabCiFile")
	defer span.End()

	if exists, _ := g.CheckFileExists(ctx, gr, ".gitlab-ci.yaml"); !exists {
		_, _, err := g.client.RepositoryFiles.CreateFile(gr.ProjectId, ".gitlab-ci.yaml", &gitlab.CreateFileOptions{
			Branch:        gitlab.Ptr("main"),
			CommitMessage: gitlab.Ptr("Add .gitlab-ci.yaml"),
			Content:       gitlab.Ptr(content),
		})
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
		return nil
	}
	_, _, err := g.client.RepositoryFiles.UpdateFile(gr.ProjectId, ".gitlab-ci.yaml", &gitlab.UpdateFileOptions{
		Branch:        gitlab.Ptr("main"),
		CommitMessage: gitlab.Ptr("Update .gitlab-ci.yaml"),
		Content:       gitlab.Ptr(content),
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (g *GitlabInfo) AddGitlabReadmeFile(ctx context.Context, gr *GitlabResp, content string) error {
	ctx, span := otel.Tracer("gitlab").Start(ctx, "AddGitlabReadmeFile")
	defer span.End()

	tpl, err := template.New("readme").Parse(content)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	var buf bytes.Buffer
	if err = tpl.Execute(&buf, map[string]interface{}{
		"ProjectName": gr.ProjectName,
	}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	if exists, _ := g.CheckFileExists(ctx, gr, "README.md"); !exists {
		_, _, err = g.client.RepositoryFiles.CreateFile(gr.ProjectId, "README.md", &gitlab.CreateFileOptions{
			Branch:        gitlab.Ptr("main"),
			CommitMessage: gitlab.Ptr("Add README.md"),
			Content:       gitlab.Ptr(buf.String()),
		})
	} else {
		_, _, err = g.client.RepositoryFiles.UpdateFile(gr.ProjectId, "README.md", &gitlab.UpdateFileOptions{
			Branch:        gitlab.Ptr("main"),
			CommitMessage: gitlab.Ptr("Update README.md"),
			Content:       gitlab.Ptr(buf.String()),
		})
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (g *GitlabInfo) CheckFileExists(ctx context.Context, gr *GitlabResp, filePath string) (bool, error) {
	_, span := otel.Tracer("gitlab").Start(ctx, "CheckFileExists")
	defer span.End()

	_, _, err := g.client.RepositoryFiles.GetFile(gr.ProjectId, filePath, &gitlab.GetFileOptions{
		Ref: gitlab.Ptr("main"),
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (g *GitlabInfo) ListVariables(ctx context.Context, gr *GitlabResp) ([]*gitlab.ProjectVariable, error) {
	ctx, span := otel.Tracer("gitlab").Start(ctx, "ListVariables")
	defer span.End()

	vars, _, err := g.client.ProjectVariables.ListVariables(gr.ProjectId, &gitlab.ListProjectVariablesOptions{})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	return vars, nil
}

func (g *GitlabInfo) CreateVariable(ctx context.Context, gr *GitlabResp, v *GitlabVariable) error {
	ctx, span := otel.Tracer("gitlab").Start(ctx, "CreateVariable")
	defer span.End()

	_, _, err := g.client.ProjectVariables.CreateVariable(gr.ProjectId, &gitlab.CreateProjectVariableOptions{
		Key:       &v.Key,
		Value:     &v.Value,
		Protected: gitlab.Ptr(false),
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

func (g *GitlabInfo) UpdateVariable(ctx context.Context, gr *GitlabResp, variable *gitlab.ProjectVariable) error {
	ctx, span := otel.Tracer("gitlab").Start(ctx, "UpdateVariable")
	defer span.End()

	switch variable.VariableType {
	case gitlab.FileVariableType:
		content := strings.Split(variable.Value, ":")
		content = append(content, gr.ProjectId)
		updatedVal := strings.Join(content, ":")
		_, _, err := g.client.ProjectVariables.UpdateVariable(gr.ProjectId, variable.Key, &gitlab.UpdateProjectVariableOptions{
			Value: &updatedVal,
		})
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	default:
		_, _, err := g.client.ProjectVariables.UpdateVariable(gr.ProjectId, variable.Key, &gitlab.UpdateProjectVariableOptions{
			Value: &gr.ProjectId,
		})
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	}
	return nil
}
