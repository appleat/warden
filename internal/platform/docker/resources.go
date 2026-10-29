package docker

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

func imageListOptions() image.ListOptions {
	return image.ListOptions{All: false}
}

type ImageItem struct {
	ID         string   `json:"id"`
	Tags       []string `json:"tags"`
	Size       int64    `json:"size"`
	Created    int64    `json:"created"`
	Containers int64    `json:"containers"`
}

func (s *Service) Images(ctx context.Context) ([]ImageItem, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, err
	}
	list, err := cli.ImageList(ctx, imageListOptions())
	if err != nil {
		return nil, err
	}
	out := make([]ImageItem, 0, len(list))
	for _, im := range list {
		tags := im.RepoTags
		if len(tags) == 0 {
			tags = []string{"<none>:<none>"}
		}
		out = append(out, ImageItem{
			ID: shortID(strings.TrimPrefix(im.ID, "sha256:")), Tags: tags,
			Size: im.Size, Created: im.Created, Containers: im.Containers,
		})
	}
	return out, nil
}

// PullQuiet 同步拉取镜像并丢弃进度信息。
func (s *Service) PullQuiet(ctx context.Context, ref string) error {
	rc, err := s.PullStream(ctx, ref)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

// PullStream 返回镜像拉取的进度流（JSON 行）。
func (s *Service) PullStream(ctx context.Context, ref string) (io.ReadCloser, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, err
	}
	return cli.ImagePull(ctx, ref, image.PullOptions{})
}

func (s *Service) RemoveImage(ctx context.Context, id string, force bool) error {
	cli, err := s.Client()
	if err != nil {
		return err
	}
	_, err = cli.ImageRemove(ctx, id, image.RemoveOptions{Force: force, PruneChildren: true})
	return err
}

type VolumeItem struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Mountpoint string `json:"mountpoint"`
	CreatedAt  string `json:"created_at"`
	Scope      string `json:"scope"`
}

func (s *Service) Volumes(ctx context.Context) ([]VolumeItem, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, err
	}
	resp, err := cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]VolumeItem, 0, len(resp.Volumes))
	for _, v := range resp.Volumes {
		if v == nil {
			continue
		}
		out = append(out, VolumeItem{
			Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint,
			CreatedAt: v.CreatedAt, Scope: v.Scope,
		})
	}
	return out, nil
}

type NetworkItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Scope      string `json:"scope"`
	Internal   bool   `json:"internal"`
	Containers int    `json:"containers"`
}

func (s *Service) Networks(ctx context.Context) ([]NetworkItem, error) {
	cli, err := s.Client()
	if err != nil {
		return nil, err
	}
	list, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]NetworkItem, 0, len(list))
	for _, n := range list {
		out = append(out, NetworkItem{
			ID: shortID(n.ID), Name: n.Name, Driver: n.Driver, Scope: n.Scope,
			Internal: n.Internal, Containers: len(n.Containers),
		})
	}
	return out, nil
}

// ProgressLine 用于前端展示拉取进度。
type ProgressLine struct {
	Status string `json:"status"`
	ID     string `json:"id"`
	Detail string `json:"detail"`
	Error  string `json:"error"`
}

func ParseProgress(b []byte) (ProgressLine, bool) {
	var p struct {
		Status         string `json:"status"`
		ID             string `json:"id"`
		ProgressDetail struct {
			Current int64 `json:"current"`
			Total   int64 `json:"total"`
		} `json:"progressDetail"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return ProgressLine{}, false
	}
	detail := p.Status
	if p.ProgressDetail.Total > 0 {
		detail = p.Status
	}
	return ProgressLine{Status: p.Status, ID: p.ID, Detail: detail, Error: p.Error}, true
}
