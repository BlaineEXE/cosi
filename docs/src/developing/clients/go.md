# Developing Client Apps in Go

This guide shows how to build Go clients that consume COSI `BucketAccess` Secrets for S3, Azure Blob, and GCS using a factory pattern. The code samples follow the v1alpha2 API, not the older v1alpha1 JSON formats.

All configuration is read from environment variables populated from a `BucketAccess` Secret. Concrete implementations are hidden behind an interface and selected by a factory based on `COSI_PROTOCOL`.

## BucketAccess Secret Data

When a `BucketAccess` is provisioned, COSI creates a Secret whose `data` contains:

- Top-level keys for all protocols:
  - `COSI_PROTOCOL` (required): one of `S3`, `Azure`, or `GCS`.
  - `COSI_CERTIFICATE_AUTHORITY` (optional): PEM-encoded CA to trust when talking to the object store endpoint.

- S3 keys:
  - Bucket info: `AWS_ENDPOINT_URL`, `BUCKET_NAME`, `AWS_DEFAULT_REGION`, `AWS_S3_ADDRESSING_STYLE`.
  - Credentials: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`.

- Azure Blob keys:
  - Bucket info: `AZURE_STORAGE_ACCOUNT`.
  - Credentials: `AZURE_STORAGE_SAS_TOKEN`, `AZURE_STORAGE_SAS_TOKEN_EXPIRY_TIMESTAMP`.

- GCS keys:
  - Bucket info: `PROJECT_ID`, `BUCKET_NAME`.
  - Credentials: `SERVICE_ACCOUNT_NAME`, `CLIENT_EMAIL`, `CLIENT_ID`, `PRIVATE_KEY_ID`, `PRIVATE_KEY`, `HMAC_ACCESS_ID`, `HMAC_SECRET`.

## Wiring the Secret into a Pod

You usually expose the Secret to your pod as environment variables:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: example-app
spec:
  template:
    spec:
      containers:
      - name: app
        image: example/app:latest
        envFrom:
        - secretRef:
            name: my-bucketaccess-secret
```

If `COSI_CERTIFICATE_AUTHORITY` is set and you want to trust a custom CA, mount it as a file instead of an env var and configure your HTTP / gRPC client to use it. This guide focuses on env-based configuration with access to a single bucket.

## Storage Interface and Factory

Define a minimal interface and a factory that reads configuration from the environment and returns a protocol-specific implementation.

```go
// import "example.com/cosi-client/storage"
package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// Storage is a tiny abstraction over object storage.
type Storage interface {
	Delete(ctx context.Context, key string) error
	Get(ctx context.Context, key string, w io.Writer) error
	Put(ctx context.Context, key string, r io.Reader, size int64) error
}

// NewFromEnv builds a Storage implementation using the env vars
// defined for BucketAccess Secrets in the COSI API.
func NewFromEnv(ctx context.Context) (Storage, error) {
	protocol := strings.ToUpper(os.Getenv("COSI_PROTOCOL"))
	if protocol == "" {
		return nil, fmt.Errorf("COSI_PROTOCOL env var must be set")
	}

	switch protocol {
	case "S3":
		return newS3FromEnv(ctx)
	case "AZURE":
		return newAzureFromEnv(ctx)
	case "GCS":
		return newGCSFromEnv(ctx)
	default:
		return nil, fmt.Errorf("unsupported COSI_PROTOCOL %q", protocol)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}
```

The concrete types (`s3Storage`, `azureStorage`, `gcsStorage`) are internal to the package and not exposed outside. Callers depend only on the `Storage` interface and the `NewFromEnv` factory.

## S3 with github.com/aws/aws-sdk-go-v2

For S3, use the official `github.com/aws/aws-sdk-go-v2` package. Credentials and configuration come from the BucketAccess Secret via env vars:

- `AWS_ENDPOINT_URL`
- `BUCKET_NAME`
- `AWS_DEFAULT_REGION`
- `AWS_S3_ADDRESSING_STYLE` ("path" or "virtual")
- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`

```go
// import "example.com/cosi-client/storage"
package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3Storage struct {
	client *s3.Client
	bucket string
}

func newS3FromEnv(ctx context.Context) (Storage, error) {
	endpoint := mustEnv("AWS_ENDPOINT_URL")
	bucket := mustEnv("BUCKET_NAME")
	region := mustEnv("AWS_DEFAULT_REGION")
	accessKey := mustEnv("AWS_ACCESS_KEY_ID")
	secretKey := mustEnv("AWS_SECRET_ACCESS_KEY")

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	usePathStyle := strings.EqualFold(os.Getenv("AWS_S3_ADDRESSING_STYLE"), "path")
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = usePathStyle
	})

	return &s3Storage{client: client, bucket: bucket}, nil
}

func (s *s3Storage) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

func (s *s3Storage) Get(ctx context.Context, key string, w io.Writer) error {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	_, err = io.Copy(w, out.Body)
	return err
}

func (s *s3Storage) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   r,
	})
	return err
}
```

This implementation uses static credentials from the Secret. In more advanced setups you can combine this with IAM or EKS IRSA, but that is outside the scope of this guide.

## Azure Blob with github.com/Azure/azure-sdk-for-go

For Azure Blob, use `github.com/Azure/azure-sdk-for-go/sdk/storage/azblob`.

From the BucketAccess Secret you use:

- `AZURE_STORAGE_ACCOUNT`
- `AZURE_STORAGE_SAS_TOKEN`
- `AZURE_STORAGE_SAS_TOKEN_EXPIRY_TIMESTAMP` (optional, informational)

```go
// import "example.com/cosi-client/storage"
package storage

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

type azureStorage struct {
	client    *azblob.Client
	container string
}

func newAzureFromEnv(ctx context.Context) (Storage, error) {
	account := mustEnv("AZURE_STORAGE_ACCOUNT")
	sasURI := mustEnv("AZURE_STORAGE_SAS_TOKEN")

	cli, err := azblob.NewClientWithNoCredential(sasURI, nil)
	if err != nil {
		return nil, fmt.Errorf("create Azure client: %w", err)
	}

	// If BUCKET_NAME is present, treat it as container name.
	// Otherwise assume the container is encoded in sasURI.
	container := os.Getenv("BUCKET_NAME")
	if container == "" {
		container = account
	}

	return &azureStorage{client: cli, container: container}, nil
}

func (a *azureStorage) Delete(ctx context.Context, key string) error {
	_, err := a.client.DeleteBlob(ctx, a.container, key, nil)
	return err
}

func (a *azureStorage) Get(ctx context.Context, key string, w io.Writer) error {
	resp, err := a.client.DownloadStream(ctx, a.container, key, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

func (a *azureStorage) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	_, err := a.client.UploadStream(ctx, a.container, key, r, nil)
	return err
}
```

## GCS with cloud.google.com/go/storage

For GCS, use `cloud.google.com/go/storage`. A simple and portable approach is:

- Prefer Application Default Credentials (for `AuthenticationType=ServiceAccount` paths where your pod's ServiceAccount is mapped to a cloud identity).
- If `PRIVATE_KEY` and related fields are present, build a minimal in-memory service-account JSON and configure the client explicitly.

From the BucketAccess Secret you use at minimum:

- `PROJECT_ID`
- `BUCKET_NAME`

Optionally:

- `CLIENT_EMAIL`, `CLIENT_ID`, `PRIVATE_KEY_ID`, `PRIVATE_KEY` for service-account based auth.

```go
// import "example.com/cosi-client/storage"
package storage

import (
	"cloud.google.com/go/storage"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"google.golang.org/api/option"
)

type gcsStorage struct {
	client *storage.Client
	bucket *storage.BucketHandle
}

func newGCSFromEnv(ctx context.Context) (Storage, error) {
	projectID := mustEnv("PROJECT_ID")
	bucketName := mustEnv("BUCKET_NAME")

	var opts []option.ClientOption
	if pk := os.Getenv("PRIVATE_KEY"); pk != "" {
		cred := map[string]string{
			"type":          "service_account",
			"project_id":    projectID,
			"private_key_id": os.Getenv("PRIVATE_KEY_ID"),
			"private_key":    pk,
			"client_email":   os.Getenv("CLIENT_EMAIL"),
			"client_id":      os.Getenv("CLIENT_ID"),
		}
		b, err := json.Marshal(cred)
		if err != nil {
			return nil, fmt.Errorf("marshal GCS credentials: %w", err)
		}
		opts = append(opts, option.WithCredentialsJSON(b))
	}

	cli, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create GCS client: %w", err)
	}

	return &gcsStorage{
		client: cli,
		bucket: cli.Bucket(bucketName),
	}, nil
}

func (g *gcsStorage) Delete(ctx context.Context, key string) error {
	return g.bucket.Object(key).Delete(ctx)
}

func (g *gcsStorage) Get(ctx context.Context, key string, w io.Writer) error {
	r, err := g.bucket.Object(key).NewReader(ctx)
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.Copy(w, r)
	return err
}

func (g *gcsStorage) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	w := g.bucket.Object(key).NewWriter(ctx)
	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}
```

## Using the Factory in Your Application

With the interface and factory in place, using object storage from your Go code is straightforward:

```go
package main

import (
	"context"
	"log"
	"strings"

	"example.com/cosi-client/storage"
)

func main() {
	ctx := context.Background()

	client, err := storage.NewFromEnv(ctx)
	if err != nil {
		log.Fatalf("create storage client: %v", err)
	}

	// Example write
	if err := client.Put(ctx, "example/key.txt", strings.NewReader("hello"), int64(len("hello"))); err != nil {
		log.Fatalf("put object: %v", err)
	}

	// Example read
	var buf strings.Builder
	if err := client.Get(ctx, "example/key.txt", &buf); err != nil {
		log.Fatalf("get object: %v", err)
	}
	log.Printf("read data: %s", buf.String())
}
```

This pattern keeps your application code independent of any specific object storage provider while remaining aligned with the v1alpha2 COSI API.
