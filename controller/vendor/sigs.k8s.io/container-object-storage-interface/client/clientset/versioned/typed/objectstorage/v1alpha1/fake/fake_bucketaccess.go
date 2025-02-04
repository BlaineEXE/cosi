/*
Copyright 2024 The Kubernetes Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
	v1alpha1 "sigs.k8s.io/container-object-storage-interface/client/apis/objectstorage/v1alpha1"
)

// FakeBucketAccesses implements BucketAccessInterface
type FakeBucketAccesses struct {
	Fake *FakeObjectstorageV1alpha1
	ns   string
}

var bucketaccessesResource = v1alpha1.SchemeGroupVersion.WithResource("bucketaccesses")

var bucketaccessesKind = v1alpha1.SchemeGroupVersion.WithKind("BucketAccess")

// Get takes name of the bucketAccess, and returns the corresponding bucketAccess object, and an error if there is any.
func (c *FakeBucketAccesses) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha1.BucketAccess, err error) {
	emptyResult := &v1alpha1.BucketAccess{}
	obj, err := c.Fake.
		Invokes(testing.NewGetActionWithOptions(bucketaccessesResource, c.ns, name, options), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.BucketAccess), err
}

// List takes label and field selectors, and returns the list of BucketAccesses that match those selectors.
func (c *FakeBucketAccesses) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha1.BucketAccessList, err error) {
	emptyResult := &v1alpha1.BucketAccessList{}
	obj, err := c.Fake.
		Invokes(testing.NewListActionWithOptions(bucketaccessesResource, bucketaccessesKind, c.ns, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha1.BucketAccessList{ListMeta: obj.(*v1alpha1.BucketAccessList).ListMeta}
	for _, item := range obj.(*v1alpha1.BucketAccessList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested bucketAccesses.
func (c *FakeBucketAccesses) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchActionWithOptions(bucketaccessesResource, c.ns, opts))

}

// Create takes the representation of a bucketAccess and creates it.  Returns the server's representation of the bucketAccess, and an error, if there is any.
func (c *FakeBucketAccesses) Create(ctx context.Context, bucketAccess *v1alpha1.BucketAccess, opts v1.CreateOptions) (result *v1alpha1.BucketAccess, err error) {
	emptyResult := &v1alpha1.BucketAccess{}
	obj, err := c.Fake.
		Invokes(testing.NewCreateActionWithOptions(bucketaccessesResource, c.ns, bucketAccess, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.BucketAccess), err
}

// Update takes the representation of a bucketAccess and updates it. Returns the server's representation of the bucketAccess, and an error, if there is any.
func (c *FakeBucketAccesses) Update(ctx context.Context, bucketAccess *v1alpha1.BucketAccess, opts v1.UpdateOptions) (result *v1alpha1.BucketAccess, err error) {
	emptyResult := &v1alpha1.BucketAccess{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateActionWithOptions(bucketaccessesResource, c.ns, bucketAccess, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.BucketAccess), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeBucketAccesses) UpdateStatus(ctx context.Context, bucketAccess *v1alpha1.BucketAccess, opts v1.UpdateOptions) (result *v1alpha1.BucketAccess, err error) {
	emptyResult := &v1alpha1.BucketAccess{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceActionWithOptions(bucketaccessesResource, "status", c.ns, bucketAccess, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.BucketAccess), err
}

// Delete takes name of the bucketAccess and deletes it. Returns an error if one occurs.
func (c *FakeBucketAccesses) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(bucketaccessesResource, c.ns, name, opts), &v1alpha1.BucketAccess{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeBucketAccesses) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewDeleteCollectionActionWithOptions(bucketaccessesResource, c.ns, opts, listOpts)

	_, err := c.Fake.Invokes(action, &v1alpha1.BucketAccessList{})
	return err
}

// Patch applies the patch and returns the patched bucketAccess.
func (c *FakeBucketAccesses) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.BucketAccess, err error) {
	emptyResult := &v1alpha1.BucketAccess{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(bucketaccessesResource, c.ns, name, pt, data, opts, subresources...), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.BucketAccess), err
}
