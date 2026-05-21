package main

import (
	"context"
	"fmt"
	"gitlab-vault/gitlab"
	"gitlab-vault/observability"
	"gitlab-vault/vault"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"sync"
	"syscall"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"go.opentelemetry.io/otel"
	"github.com/spf13/pflag"
)

type GitopsInfo struct {
	ClusterName string
	ProductLine string
	GitlabNs    string
	VaultAddr   string
	AuthType    string
}

type ProfilingInfo struct {
	CpuProfile string
	MemProfile string
}

var k = koanf.New(".")

const numWorkers = 10

func main() {
	gi, prof := loadConfig()

	stopProfiling := startProfiling(prof)
	defer stopProfiling()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdown, err := observability.InitTracer(ctx, "vault-gitlab", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if err != nil {
		log.Fatalf("could not initialize tracer: %v", err)
	}
	defer shutdown(ctx)

	tracer := otel.Tracer("vault-gitlab")
	ctx, span := tracer.Start(ctx, "run")
	defer span.End()

	fmt.Printf("Gitops Info: %+v\n", gi)

	if err := validateEnvVars(gi.AuthType); err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	vaultAddr := gi.VaultAddr
	roleID := os.Getenv("role_id")
	secretID := os.Getenv("secret_id")
	vaultToken := os.Getenv("vault_token")
	gitlabURL := os.Getenv("gitlab_url")

	var creds vault.GetCreds

	switch gi.AuthType {
	case "approle":
		if gi.ProductLine == "prd" {
			creds = vault.NewCredsApprole(vaultAddr, "mor/prod/gitlab", roleID, secretID)
		} else {
			creds = vault.NewCredsApprole(vaultAddr, "mor/stg/gitlab", roleID, secretID)
		}
	case "token":
		if gi.ProductLine == "prd" {
			creds = vault.NewCreds(vaultAddr, "mor/prod/gitlab", vaultToken)
		} else {
			creds = vault.NewCreds(vaultAddr, "mor/stg/gitlab", vaultToken)
		}
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Println("getting Vault token...")
	resp, err := creds.RetrieveCreds(ctx)
	if err != nil {
		log.Fatalf("could not get credentials: %v", err)
	}

	if resp.Token == nil {
		log.Fatal("no token received from Vault")
	}
	token, ok := resp.Token["token"].(string)
	if !ok || token == "" {
		log.Fatal("invalid or empty token received from Vault")
	}
	log.Println("successfully got Vault token")

	gitlabInfo := &gitlab.GitlabInfo{
		Token:    token,
		BaseURL:  gitlabURL,
		GitlabNs: gi.GitlabNs,
	}
	if err := gitlabInfo.Init(ctx); err != nil {
		log.Fatalf("could not initialize GitLab client: %v", err)
	}

	log.Println("listing GitLab projects...")
	projects, err := gitlabInfo.ListProject(ctx)
	if err != nil {
		log.Fatalf("could not list projects: %v", err)
	}
	log.Printf("found %d projects", len(projects))

	projectChan := make(chan *gitlab.GitlabResp, len(projects))
	errorChan := make(chan error, len(projects))

	var wg sync.WaitGroup

	workers := numWorkers
	if len(projects) < workers {
		workers = len(projects)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for project := range projectChan {
				log.Printf("worker %d processing project: %s", workerID, project.ProjectName)

				if err := gitlabInfo.AddGitlabCiFile(ctx, project, k.String("gitlab-ci-content")); err != nil {
					errorChan <- fmt.Errorf("could not add .gitlab-ci.yaml for %s: %w", project.ProjectName, err)
					continue
				}

				if err := gitlabInfo.AddGitlabReadmeFile(ctx, project, k.String("gitlab-readme-content")); err != nil {
					errorChan <- fmt.Errorf("could not add README.md for %s: %w", project.ProjectName, err)
					continue
				}

				vars, err := gitlabInfo.ListVariables(ctx, project)
				if err != nil {
					errorChan <- fmt.Errorf("could not list variables for %s: %w", project.ProjectName, err)
					continue
				}

				for _, v := range vars {
					if err := gitlabInfo.UpdateVariable(ctx, project, v); err != nil {
						errorChan <- fmt.Errorf("could not update variable %s for %s: %w", v.Key, project.ProjectName, err)
					}
				}
			}
		}(i)
	}

	go func() {
		for _, project := range projects {
			projectChan <- project
		}
		close(projectChan)
	}()

	go func() {
		wg.Wait()
		close(errorChan)
	}()

	var errs []error
loop:
	for {
		select {
		case err, ok := <-errorChan:
			if !ok {
				break loop
			}
			errs = append(errs, err)
		case sig := <-sigChan:
			log.Printf("received signal %s, exiting...", sig)
			cancel()
			return
		}
	}

	if len(errs) > 0 {
		log.Printf("completed with %d errors:", len(errs))
		for _, err := range errs {
			log.Println(err)
		}
	} else {
		log.Println("successfully processed all projects")
	}
}

func validateEnvVars(authType string) error {
	if os.Getenv("gitlab_url") == "" {
		return fmt.Errorf("gitlab_url is not set")
	}
	switch authType {
	case "token":
		if os.Getenv("vault_token") == "" {
			return fmt.Errorf("vault_token is required for token auth")
		}
	case "approle":
		if os.Getenv("role_id") == "" || os.Getenv("secret_id") == "" {
			return fmt.Errorf("role_id and secret_id are required for approle auth")
		}
	default:
		return fmt.Errorf("unknown auth_type %q: must be token or approle", authType)
	}
	return nil
}

func loadConfig() (*GitopsInfo, *ProfilingInfo) {
	f := file.Provider("conf/config.yaml")
	log.Printf("loading config from %v", f)
	if err := k.Load(f, yaml.Parser()); err != nil {
		log.Fatalf("error loading config: %v", err)
	}

	cmd := pflag.NewFlagSet("config", pflag.ExitOnError)
	cmd.Usage = func() {
		fmt.Println(cmd.FlagUsages())
		os.Exit(0)
	}
	cmd.String("product_line", "stg", "product line to deploy (prd, stg)")
	cmd.String("cluster_name", "test1", "the cluster name to deploy")
	cmd.String("auth_type", " ", "the authentication type (vault token or approle)")
	cmd.String("cpu_profile", "", "write cpu profile to this file")
	cmd.String("mem_profile", "", "write memory profile to this file")
	cmd.Parse(os.Args[1:])

	if err := k.Load(posflag.Provider(cmd, ".", k), nil); err != nil {
		log.Fatalf("error loading config: %v", err)
	}

	gi := &GitopsInfo{}
	switch k.String("product_line") {
	case "prd":
		gi = &GitopsInfo{
			ProductLine: k.String("product_line"),
			ClusterName: k.String("cluster_name"),
			GitlabNs:    k.String("zone.production.gitlab_namespace"),
			VaultAddr:   k.String("zone.production.vault_addr"),
			AuthType:    k.String("auth_type"),
		}
	case "stg":
		gi = &GitopsInfo{
			ProductLine: k.String("product_line"),
			ClusterName: k.String("cluster_name"),
			GitlabNs:    k.String("zone.development.gitlab_namespace"),
			VaultAddr:   k.String("zone.development.vault_addr"),
			AuthType:    k.String("auth_type"),
		}
	}

	profiling := &ProfilingInfo{
		CpuProfile: k.String("cpu_profile"),
		MemProfile: k.String("mem_profile"),
	}
	return gi, profiling
}

func startProfiling(prof *ProfilingInfo) func() {
	var cpuFile *os.File

	if prof.CpuProfile != "" {
		log.Printf("CPU profiling enabled, writing to %s", prof.CpuProfile)
		f, err := os.Create(prof.CpuProfile)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			f.Close()
			log.Fatal(err)
		}
		cpuFile = f
	}

	return func() {
		if cpuFile != nil {
			pprof.StopCPUProfile()
			cpuFile.Close()
		}
		if prof.MemProfile != "" {
			log.Printf("memory profiling enabled, writing to %s", prof.MemProfile)
			f, err := os.Create(prof.MemProfile)
			if err != nil {
				log.Fatal(err)
			}
			defer f.Close()
			runtime.GC()
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Fatal(err)
			}
		}
	}
}
