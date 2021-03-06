/*
Copyright 2017 The Kubernetes Authors.

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

package secrets

import (
	"fmt"
	"strings"
	"time"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kops/pkg/apis/kops"
	kopsinternalversion "k8s.io/kops/pkg/client/clientset_generated/clientset/typed/kops/internalversion"
	"k8s.io/kops/pkg/pki"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/util/pkg/vfs"
)

// NamePrefix is a prefix we use to avoid collisions with other keysets
const NamePrefix = "token-"

// ClientsetSecretStore is a SecretStore backed by Keyset objects in an API server
type ClientsetSecretStore struct {
	namespace string
	clientset kopsinternalversion.KopsInterface
}

var _ fi.SecretStore = &ClientsetSecretStore{}

// NewClientsetSecretStore is the constructor for ClientsetSecretStore
func NewClientsetSecretStore(clientset kopsinternalversion.KopsInterface, namespace string) fi.SecretStore {
	c := &ClientsetSecretStore{
		clientset: clientset,
		namespace: namespace,
	}
	return c
}

// FindSecret implements fi.SecretStore::FindSecret
func (c *ClientsetSecretStore) FindSecret(name string) (*fi.Secret, error) {
	s, err := c.loadSecret(name)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ListSecrets implements fi.SecretStore::ListSecrets
func (c *ClientsetSecretStore) ListSecrets() ([]string, error) {
	list, err := c.clientset.Keysets(c.namespace).List(v1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("error listing keysets: %v", err)
	}

	var names []string
	for i := range list.Items {
		keyset := &list.Items[i]

		switch keyset.Spec.Type {
		case kops.SecretTypeSecret:
			name := strings.TrimPrefix(keyset.Name, NamePrefix)
			names = append(names, name)
		}
	}

	return names, nil
}

// Secret implements fi.SecretStore::Secret
func (c *ClientsetSecretStore) Secret(name string) (*fi.Secret, error) {
	s, err := c.FindSecret(name)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("Secret not found: %q", name)
	}
	return s, nil
}

// DeleteSecret implements fi.SecretStore::DeleteSecret
func (c *ClientsetSecretStore) DeleteSecret(item *fi.KeystoreItem) error {
	client := c.clientset.Keysets(c.namespace)
	return fi.DeleteKeysetItem(client, item.Name, kops.SecretTypeKeypair, item.Id)
}

// GetOrCreateSecret implements fi.SecretStore::GetOrCreateSecret
func (c *ClientsetSecretStore) GetOrCreateSecret(name string, secret *fi.Secret) (*fi.Secret, bool, error) {
	for i := 0; i < 2; i++ {
		s, err := c.FindSecret(name)
		if err != nil {
			return nil, false, err
		}

		if s != nil {
			return s, false, nil
		}

		_, err = c.createSecret(secret, name)
		if err != nil {
			if errors.IsAlreadyExists(err) && i == 0 {
				glog.Infof("Got already-exists error when writing secret; likely due to concurrent creation.  Will retry")
				continue
			} else {
				return nil, false, err
			}
		}

		if err == nil {
			break
		}
	}

	// Make double-sure it round-trips
	s, err := c.loadSecret(name)
	if err != nil {
		glog.Fatalf("unable to load secret immmediately after creation %v: %v", name, err)
		return nil, false, err
	}
	return s, true, nil
}

// loadSecret returns the named secret, if it exists, otherwise returns nil
func (c *ClientsetSecretStore) loadSecret(name string) (*fi.Secret, error) {
	name = NamePrefix + name
	keyset, err := c.clientset.Keysets(c.namespace).Get(name, v1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("error reading keyset %q: %v", name, err)
	}

	return parseSecret(keyset)
}

// parseSecret attempts to parse the primary secret, otherwise returns nil
func parseSecret(keyset *kops.Keyset) (*fi.Secret, error) {
	primary := fi.FindPrimary(keyset)
	if primary == nil {
		return nil, nil
	}

	s := &fi.Secret{}
	s.Data = primary.PrivateMaterial
	return s, nil
}

// createSecret writes the secret, but only if it does not exist
func (c *ClientsetSecretStore) createSecret(s *fi.Secret, name string) (*kops.Keyset, error) {
	keyset := &kops.Keyset{}
	keyset.Name = NamePrefix + name
	keyset.Spec.Type = kops.SecretTypeSecret

	t := time.Now().UnixNano()
	id := pki.BuildPKISerial(t)

	keyset.Spec.Keys = append(keyset.Spec.Keys, kops.KeysetItem{
		Id:              id.String(),
		PrivateMaterial: s.Data,
	})

	return c.clientset.Keysets(c.namespace).Create(keyset)
}

// VFSPath implements fi.SecretStore::VFSPath
func (c *ClientsetSecretStore) VFSPath() vfs.Path {
	// We will implement mirroring instead
	glog.Fatalf("ClientsetSecretStore::VFSPath not implemented")
	return nil
}
