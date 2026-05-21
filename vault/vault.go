package vault

import (
	"context"
	"log"
	"time"

	"github.com/hashicorp/vault-client-go"
	"github.com/hashicorp/vault-client-go/schema"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

type Creds struct {
	vaultAddr  string
	vaultPath  string
	vaultToken string
}

type CredsApprole struct {
	vaultAddr       string
	vaultPath       string
	approleRoleID   string
	approleSecretID string
}

type VaultResponse struct {
	Token      map[string]interface{}
	ExpireTime string
}

func NewCreds(addr, path, token string) *Creds {
	return &Creds{
		vaultAddr:  addr,
		vaultPath:  path,
		vaultToken: token,
	}
}

func NewCredsApprole(addr, path, roleID, secretID string) *CredsApprole {
	return &CredsApprole{
		vaultAddr:       addr,
		vaultPath:       path,
		approleRoleID:   roleID,
		approleSecretID: secretID,
	}
}

type GetCreds interface {
	RetrieveCreds(context.Context) (*VaultResponse, error)
}

func (c *Creds) initVault(ctx context.Context) (vault.Client, error) {
	client, err := vault.New(
		vault.WithAddress(c.vaultAddr),
		vault.WithRequestTimeout(30*time.Second),
	)
	if err != nil {
		log.Print("could not initialize vault")
		return vault.Client{}, err
	}
	if err := client.SetToken(c.vaultToken); err != nil {
		log.Print("could not set vault token")
		return vault.Client{}, err
	}
	return *client.Clone(), nil
}

func (c *CredsApprole) initVault(ctx context.Context) (vault.Client, error) {
	client, err := vault.New(
		vault.WithAddress(c.vaultAddr),
		vault.WithRequestTimeout(30*time.Second),
	)
	if err != nil {
		log.Println("could not initialize vault")
		return vault.Client{}, err
	}

	vaultToken, err := client.Auth.AppRoleLogin(ctx, schema.AppRoleLoginRequest{
		RoleId:   c.approleRoleID,
		SecretId: c.approleSecretID,
	}, vault.WithMountPath("approle"))
	if err != nil {
		log.Printf("could not retrieve token with approle: %v", err)
		return vault.Client{}, err
	}

	if vaultToken == nil || vaultToken.Auth == nil {
		log.Println("login succeeded but no auth info received")
		return vault.Client{}, err
	}
	if err := client.SetToken(vaultToken.Auth.ClientToken); err != nil {
		log.Println("could not set vault token")
		return vault.Client{}, err
	}

	return *client.Clone(), nil
}

func (c *Creds) RetrieveCreds(ctx context.Context) (*VaultResponse, error) {
	ctx, span := otel.Tracer("vault").Start(ctx, "RetrieveCreds/token")
	defer span.End()

	client, err := c.initVault(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	resp, err := client.Secrets.KvV2Read(ctx, c.vaultPath, vault.WithMountPath("secret"))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		log.Printf("could not retrieve secret at %s", c.vaultPath)
		return nil, err
	}
	return &VaultResponse{
		Token:      resp.Data.Data,
		ExpireTime: "",
	}, nil
}

func (c *CredsApprole) RetrieveCreds(ctx context.Context) (*VaultResponse, error) {
	ctx, span := otel.Tracer("vault").Start(ctx, "RetrieveCreds/approle")
	defer span.End()

	client, err := c.initVault(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	resp, err := client.Secrets.KvV2Read(ctx, c.vaultPath, vault.WithMountPath("secret"))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		log.Printf("could not retrieve secret at %s", c.vaultPath)
		return nil, err
	}
	return &VaultResponse{
		Token:      resp.Data.Data,
		ExpireTime: "",
	}, nil
}
