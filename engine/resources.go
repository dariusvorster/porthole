package engine

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"time"
)

// Volume is one entry from `container volume ls --format json` (resources spec
// §8.1). id == name. NOTE: SizeInBytes is the sparse ALLOCATED max (e.g. 512 GiB
// default), NOT actual disk usage — never render it as disk-used; use df + the
// prune "Reclaimed X" figure for real numbers.
type Volume struct {
	Name        string            `json:"name"`
	Driver      string            `json:"driver"`
	Format      string            `json:"format"`
	SizeInBytes int64             `json:"sizeInBytes"` // allocated/sparse, not usage
	Source      string            `json:"source"`
	Created     time.Time         `json:"created"`
	Labels      map[string]string `json:"labels"`
}

type rawVolume struct {
	Configuration struct {
		Name         string            `json:"name"`
		CreationDate time.Time         `json:"creationDate"`
		Driver       string            `json:"driver"`
		Format       string            `json:"format"`
		SizeInBytes  int64             `json:"sizeInBytes"`
		Source       string            `json:"source"`
		Labels       map[string]string `json:"labels"`
	} `json:"configuration"`
	ID string `json:"id"`
}

// PruneResult is the parsed output of a `*  prune` command: the runtime's
// reclaimed display string (e.g. "138,8 MB" — locale comma, kept verbatim for the
// toast) and the ids it removed.
type PruneResult struct {
	Reclaimed string   `json:"reclaimed"` // display string from "Reclaimed <X> in disk space"
	Removed   []string `json:"removed"`
}

// --- volumes -------------------------------------------------------------

func (e *CLIEngine) VolumeList(ctx context.Context) ([]Volume, error) {
	var raw []rawVolume
	if err := e.runJSON(ctx, &raw, "volume", "ls", "--format", "json"); err != nil {
		return nil, err
	}
	out := make([]Volume, 0, len(raw))
	for _, r := range raw {
		out = append(out, Volume{
			Name:        r.Configuration.Name,
			Driver:      r.Configuration.Driver,
			Format:      r.Configuration.Format,
			SizeInBytes: r.Configuration.SizeInBytes,
			Source:      r.Configuration.Source,
			Created:     r.Configuration.CreationDate,
			Labels:      r.Configuration.Labels,
		})
	}
	return out, nil
}

// VolumeRemove deletes a named volume. The runtime REFUSES if the volume is
// mounted by any container, running or stopped (→ classified ErrVolumeInUse).
// There is no --force for volumes.
func (e *CLIEngine) VolumeRemove(ctx context.Context, name string) error {
	_, err := e.run(ctx, "volume", "delete", name)
	return err
}

func (e *CLIEngine) VolumePrune(ctx context.Context) (PruneResult, error) {
	out, err := e.run(ctx, "volume", "prune")
	if err != nil {
		return PruneResult{}, err
	}
	return parsePruneOutput(out), nil
}

// --- networks ------------------------------------------------------------

func (e *CLIEngine) NetworkPrune(ctx context.Context) (PruneResult, error) {
	out, err := e.run(ctx, "network", "prune")
	if err != nil {
		return PruneResult{}, err
	}
	return parsePruneOutput(out), nil
}

// --- images --------------------------------------------------------------

// ImageRemove deletes images by ref. force only ignores not-found errors — it
// does NOT override an in-use reference, because the runtime never refuses an
// image delete (resources spec §8.3b).
func (e *CLIEngine) ImageRemove(ctx context.Context, force bool, refs ...string) error {
	args := []string{"image", "delete"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, refs...)
	_, err := e.run(ctx, args...)
	return err
}

// ImagePrune removes dangling images, or all unreferenced with all=true.
func (e *CLIEngine) ImagePrune(ctx context.Context, all bool) (PruneResult, error) {
	args := []string{"image", "prune"}
	if all {
		args = append(args, "--all")
	}
	out, err := e.run(ctx, args...)
	if err != nil {
		return PruneResult{}, err
	}
	return parsePruneOutput(out), nil
}

func (e *CLIEngine) ImageTag(ctx context.Context, src, dst string) error {
	_, err := e.run(ctx, "image", "tag", src, dst)
	return err
}

// ImagePull pulls an image standalone, streaming the same `--progress plain`
// phase counter as create (resources spec §6). Reuses RunUpdate/parsePhase: a
// terminal "created" carries the ref (no container id), "error" carries a
// classified CLIError. The child is ctx-reaped like the create/logs streams.
func (e *CLIEngine) ImagePull(ctx context.Context, ref string) <-chan RunUpdate {
	ch := make(chan RunUpdate, 16)
	send := func(u RunUpdate) {
		select {
		case ch <- u:
		case <-ctx.Done():
		}
	}
	go func() {
		defer close(ch)
		args := []string{"image", "pull", "--progress", "plain", ref}
		cmd := exec.CommandContext(ctx, e.Bin, args...)
		stderr, err := cmd.StderrPipe()
		if err != nil {
			send(RunUpdate{Kind: "error", Err: err})
			return
		}
		if err := cmd.Start(); err != nil {
			send(RunUpdate{Kind: "error", Err: err})
			return
		}
		var buf strings.Builder
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			buf.WriteString(line)
			buf.WriteByte('\n')
			if n, m, txt, ok := parsePhase(line); ok {
				send(RunUpdate{Kind: "progress", Index: n, Total: m, Phase: txt})
			}
		}
		if werr := cmd.Wait(); werr != nil {
			kind, msg := classify(buf.String())
			send(RunUpdate{Kind: "error", Err: &CLIError{Args: args, Kind: kind, Message: msg, Raw: buf.String()}})
			return
		}
		send(RunUpdate{Kind: "created", ID: ref})
	}()
	return ch
}

// --- containers ----------------------------------------------------------

// ContainerPrune removes all stopped containers (`container prune`).
func (e *CLIEngine) ContainerPrune(ctx context.Context) (PruneResult, error) {
	out, err := e.run(ctx, "prune")
	if err != nil {
		return PruneResult{}, err
	}
	return parsePruneOutput(out), nil
}

// --- helpers -------------------------------------------------------------

// parsePruneOutput parses `Reclaimed <X> in disk space\n<id>\n<id>...`. The
// reclaimed figure is kept as a display string (locale comma preserved); every
// other non-empty line is a removed id.
func parsePruneOutput(b []byte) PruneResult {
	var res PruneResult
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Reclaimed ") {
			s := strings.TrimPrefix(line, "Reclaimed ")
			s = strings.TrimSuffix(s, " in disk space")
			res.Reclaimed = strings.TrimSpace(s)
			continue
		}
		res.Removed = append(res.Removed, line)
	}
	return res
}
