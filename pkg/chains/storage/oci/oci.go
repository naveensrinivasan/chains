/*
Copyright 2020 The Tekton Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package oci

import (
	"context"
	"encoding/json"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/authn/k8schain"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/pkg/errors"
	"github.com/sigstore/cosign/pkg/cosign"
	cremote "github.com/sigstore/cosign/pkg/cosign/remote"
	"github.com/tektoncd/chains/pkg/chains/formats/simple"
	"github.com/tektoncd/chains/pkg/config"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
)

const (
	StorageBackendOCI = "oci"
)

// Backend is a storage backend that stores signed payloads in the TaskRun metadata as an annotation.
// It is stored as base64 encoded JSON.
type Backend struct {
	logger *zap.SugaredLogger
	tr     *v1beta1.TaskRun
	cfg    config.Config
	kc     authn.Keychain
	auth   remote.Option
}

// NewStorageBackend returns a new OCI StorageBackend that stores signatures in an OCI registry
func NewStorageBackend(logger *zap.SugaredLogger, client kubernetes.Interface, tr *v1beta1.TaskRun, cfg config.Config) (*Backend, error) {
	kc, err := k8schain.New(context.TODO(), client,
		k8schain.Options{Namespace: tr.Namespace, ServiceAccountName: tr.Spec.ServiceAccountName})
	if err != nil {
		return nil, err
	}

	return &Backend{
		logger: logger,
		tr:     tr,
		cfg:    cfg,
		kc:     kc,
		auth:   remote.WithAuthFromKeychain(kc),
	}, nil
}

// StorePayload implements the Payloader interface.
func (b *Backend) StorePayload(rawPayload []byte, signature string, storageOpts config.StorageOpts) error {
	b.logger.Infof("Storing payload on TaskRun %s/%s", b.tr.Namespace, b.tr.Name)

	format := simple.NewSimpleStruct()
	if err := json.Unmarshal(rawPayload, &format); err != nil {
		return errors.Wrap(err, "only OCI artifacts can be stored in an OCI registry")
	}
	imageName := format.ImageName()

	b.logger.Infof("Uploading %s signature", imageName)
	var opts []name.Option
	if b.cfg.Storage.OCI.Insecure {
		opts = append(opts, name.Insecure)
	}
	ref, err := name.NewDigest(imageName, opts...)
	if err != nil {
		return errors.Wrap(err, "getting digest")
	}
	dgst, err := v1.NewHash(ref.DigestStr())
	if err != nil {
		return errors.Wrap(err, "parsing digest")
	}
	cosignDst := cosign.AttachedImageTag(ref.Repository, dgst, cosign.SignatureTagSuffix)
	if err != nil {
		return errors.Wrap(err, "destination ref")
	}
	if _, err = cremote.UploadSignature([]byte(signature), rawPayload, cosignDst, cremote.UploadOpts{
		RemoteOpts: []remote.Option{b.auth},
		Cert:       []byte(storageOpts.Cert),
		Chain:      []byte(storageOpts.Chain),
	}); err != nil {
		return errors.Wrap(err, "uploading")
	}
	b.logger.Infof("Successfully uploaded signature for %s", imageName)
	return nil
}

func (b *Backend) Type() string {
	return StorageBackendOCI
}
