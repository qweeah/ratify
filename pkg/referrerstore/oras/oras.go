/*
Copyright The Ratify Authors.
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

package oras

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/pkg/content"
	"oras.land/oras-go/pkg/oras"

	"github.com/deislabs/ratify/pkg/common"
	"github.com/deislabs/ratify/pkg/ocispecs"
	"github.com/deislabs/ratify/pkg/referrerstore"
	"github.com/deislabs/ratify/pkg/referrerstore/config"
	"github.com/deislabs/ratify/pkg/referrerstore/factory"
	"github.com/opencontainers/go-digest"
	artifactspec "github.com/oras-project/artifacts-spec/specs-go/v1"
)

const (
	storeName             = "oras"
	defaultLocalCachePath = "~/.ratify/local_oras_cache"
)

// OrasStoreConf describes the configuration of ORAS store
type OrasStoreConf struct {
	Name           string `json:"name"`
	UseHttp        bool   `json:"useHttp,omitempty"`
	CosignEnabled  bool   `json:"cosign-enabled,omitempty"`
	AuthProvider   string `json:"auth-provider,omitempty"`
	LocalCachePath string `json:"localCachePath,omitempty"`
}

type orasStoreFactory struct{}

type orasStore struct {
	config     *OrasStoreConf
	rawConfig  config.StoreConfig
	localCache *content.OCI
}

func init() {
	factory.Register(storeName, &orasStoreFactory{})
}

func (s *orasStoreFactory) Create(version string, storeConfig config.StorePluginConfig) (referrerstore.ReferrerStore, error) {
	conf := OrasStoreConf{}

	storeConfigBytes, err := json.Marshal(storeConfig)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(storeConfigBytes, &conf); err != nil {
		return nil, fmt.Errorf("failed to parse oras store configuration: %v", err)
	}

	if conf.AuthProvider != "" {
		return nil, fmt.Errorf("auth provider %s is not supported", conf.AuthProvider)
	}

	// Set up the local cache where content will land when we pull
	if conf.LocalCachePath == "" {
		conf.LocalCachePath = defaultLocalCachePath
	}
	localRegistry, err := content.NewOCI(conf.LocalCachePath)
	if err != nil {
		return nil, fmt.Errorf("could not create local oras cache at path #{conf.LocalCachePath}: #{err}")
	}

	return &orasStore{config: &conf, rawConfig: config.StoreConfig{Version: version, Store: storeConfig}, localCache: localRegistry}, nil
}

func (store *orasStore) Name() string {
	return storeName
}

func (store *orasStore) GetConfig() *config.StoreConfig {
	return &store.rawConfig
}

func (store *orasStore) ListReferrers(ctx context.Context, subjectReference common.Reference, artifactTypes []string, nextToken string) (referrerstore.ListReferrersResult, error) {
	// TODO: handle nextToken
	registryClient, err := store.createRegistryClient(subjectReference)
	if err != nil {
		return referrerstore.ListReferrersResult{}, err
	}

	var referrerDescriptors []artifactspec.Descriptor
	if artifactTypes == nil {
		artifactTypes = []string{""}
	}
	for _, artifactType := range artifactTypes {
		_, res, err := oras.Discover(ctx, registryClient.Resolver, subjectReference.Original, artifactType)
		if err != nil {
			return referrerstore.ListReferrersResult{}, err
		}
		referrerDescriptors = append(referrerDescriptors, res...)
	}

	var referrers []ocispecs.ReferenceDescriptor
	for _, referrer := range referrerDescriptors {
		referrers = append(referrers, ArtifactDescriptorToReferenceDescriptor(referrer))
	}

	if store.config.CosignEnabled {
		cosignReferences, err := getCosignReferences(subjectReference)
		if err != nil {
			return referrerstore.ListReferrersResult{}, err
		}
		referrers = append(referrers, *cosignReferences...)
	}

	return referrerstore.ListReferrersResult{Referrers: referrers}, nil
}

func (store *orasStore) GetBlobContent(ctx context.Context, subjectReference common.Reference, digest digest.Digest) ([]byte, error) {
	registryClient, err := store.createRegistryClient(subjectReference)
	if err != nil {
		return nil, err
	}

	ref := fmt.Sprintf("%s@%s", subjectReference.Path, digest)
	desc, err := oras.Copy(ctx, registryClient, ref, store.localCache, "")
	if err != nil {
		return nil, err
	}

	return store.getRawContentFromCache(ctx, desc)
}

func (store *orasStore) GetReferenceManifest(ctx context.Context, subjectReference common.Reference, referenceDesc ocispecs.ReferenceDescriptor) (ocispecs.ReferenceManifest, error) {
	ref, err := name.ParseReference(fmt.Sprintf("%s@%s", subjectReference.Path, referenceDesc.Digest))
	if err != nil {
		return ocispecs.ReferenceManifest{}, err
	}
	dig, err := remote.Get(ref)
	if err != nil {
		return ocispecs.ReferenceManifest{}, err
	}
	var manifest = artifactspec.Manifest{}
	if err := json.Unmarshal(dig.Manifest, &manifest); err != nil {
		return ocispecs.ReferenceManifest{}, err
	}

	return ArtifactManifestToReferenceManifest(manifest), nil
}

func (store *orasStore) GetSubjectDescriptor(ctx context.Context, subjectReference common.Reference) (*ocispecs.SubjectDescriptor, error) {
	ref, err := name.ParseReference(subjectReference.Original)
	if err != nil {
		return nil, err
	}
	dig, err := remote.Head(ref)
	if err != nil {
		return nil, err
	}

	dg, err := digest.Parse(dig.Digest.String())
	if err != nil {
		return nil, err
	}

	desc := oci.Descriptor{
		MediaType: string(dig.MediaType),
		Digest:    dg,
		Size:      dig.Size,
		URLs:      dig.URLs,
	}
	return &ocispecs.SubjectDescriptor{Descriptor: desc}, nil
}

func (store *orasStore) createRegistryClient(targetRef common.Reference) (*content.Registry, error) {
	// TODO: support authentication
	registryOpts := content.RegistryOptions{
		Configs:   nil,
		Username:  "",
		Password:  "",
		Insecure:  isInsecureRegistry(targetRef.Original, store.config),
		PlainHTTP: store.config.UseHttp,
	}
	return content.NewRegistryWithDiscover(targetRef.Original, registryOpts)
}

func (store *orasStore) getRawContentFromCache(ctx context.Context, descriptor oci.Descriptor) ([]byte, error) {
	reader, err := store.localCache.Fetch(ctx, descriptor)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, descriptor.Size)
	_, err = reader.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}