package stacks

import (
	"context"
	"strconv"
	"strings"

	"github.com/porthole/porthole/engine"
)

// remediate.go applies the DESTRUCTIVE recreate the planner flags (Phase 10).
// Because the runtime has id==name strict and no rename (capture), create-then-
// swap can't end on the canonical name — so the only path is stop-remove-create,
// which opens a window where the service is gone. The safety is a spec snapshot
// taken BEFORE the remove: if the new container can't be created/started, the old
// one is re-created from the snapshot and the failure is surfaced loudly. The
// whole snapshot→remove→create→start runs under the shared per-id lock so the
// supervisor/discovery/other mutations can't act in the gone-window.

// RemediateOutcome is the per-service result of an apply.
type RemediateOutcome string

const (
	OutcomeRecreated  RemediateOutcome = "recreated"   // new container up
	OutcomeRolledBack RemediateOutcome = "rolled_back" // create/start failed → previous restored (LOUD)
	OutcomeFailedDown RemediateOutcome = "failed_down" // rollback ALSO failed → service DOWN (critical)
)

// ServiceRemediation is one service's recreate result.
type ServiceRemediation struct {
	Service string           `json:"service"`
	Outcome RemediateOutcome `json:"outcome"`
	Message string           `json:"message,omitempty"`
	Volumes []VolumePlan     `json:"volumes,omitempty"` // what the recreate preserved/orphaned
}

// RemediateResult is the outcome of applying all flagged recreates for a stack.
type RemediateResult struct {
	Stack   string               `json:"stack"`
	Applied []ServiceRemediation `json:"applied"`
}

// Remediate applies the recreate actions the planner flags for a stack — one
// drifted service at a time, each isolated (a rollback in one never aborts the
// others). Read-only until called: the diff/plan endpoints never reach here.
func (e *Executor) Remediate(ctx context.Context, stack Stack) (RemediateResult, error) {
	unlock := e.stackLocks.Lock(stack.Name)
	defer unlock()

	observed, err := e.eng.ListContainers(ctx, true)
	if err != nil {
		return RemediateResult{}, err
	}
	plan := PlanReconcile(stack, observed)

	svcByName := map[string]Service{}
	for _, s := range stack.Services {
		svcByName[s.Name] = s
	}
	members, err := e.members(ctx, stack.Name)
	if err != nil {
		return RemediateResult{}, err
	}

	result := RemediateResult{Stack: stack.Name}
	for _, a := range plan.Actions {
		if a.Action != ActionRecreate {
			continue // recreate only — never orphan, never the safe actions
		}
		old, ok := members[a.Service]
		if !ok {
			continue
		}
		result.Applied = append(result.Applied, e.recreateService(ctx, stack, svcByName[a.Service], old))
	}
	return result, nil
}

// recreateService runs the destructive sequence for ONE service under the shared
// per-id lock, held for the WHOLE sequence (snapshot→remove→create→start) so
// nothing else can touch the container during the gone-window.
func (e *Executor) recreateService(ctx context.Context, stack Stack, svc Service, old engine.Container) ServiceRemediation {
	unlock := e.locks.Lock(old.ID)
	defer unlock()

	snapshot := e.snapshotFor(old) // restore source — the stored spec if we have one, else reconstructed
	newSpec := runSpecFor(stack, svc)
	plan := planRecreate(snapshot, newSpec)
	rem := ServiceRemediation{Service: svc.Name, Volumes: plan.Volumes}

	// Remove the old (force = stop+remove). If this fails the old is still present,
	// so the service was never lost — report it without a gone-window.
	if err := e.eng.DeleteContainer(ctx, old.ID, true); err != nil {
		rem.Outcome = OutcomeRolledBack
		rem.Message = "could not remove the previous container; left it running: " + err.Error()
		return rem
	}

	// From here the old is GONE — every failure path must restore it.
	if _, err := e.eng.RunContainer(ctx, newSpec); err != nil {
		return e.rollback(ctx, rem, snapshot, "create failed: "+err.Error())
	}
	if err := e.eng.StartContainer(ctx, newSpec.Name); err != nil {
		// New container exists but won't start: remove it, then restore the old.
		_ = e.eng.DeleteContainer(ctx, newSpec.Name, true)
		return e.rollback(ctx, rem, snapshot, "new container failed to start: "+err.Error())
	}
	e.saveSpec(newSpec.Name, newSpec) // success → keep current so the NEXT rollback targets this version
	rem.Outcome = OutcomeRecreated
	return rem
}

// snapshotFor returns the rollback source for a container: its persisted
// authoritative RunSpec when one exists (byte-perfect — keeps entrypoint/shm-size/
// tmpfs the observed object can't recover), else a reconstruction from the
// observed container (the additive fallback — never worse than before).
func (e *Executor) snapshotFor(old engine.Container) engine.RunSpec {
	if e.specs != nil {
		if spec, ok, err := e.specs.GetSpec(old.ID); err == nil && ok {
			return spec
		}
	}
	return specFromContainer(old)
}

func (e *Executor) saveSpec(name string, spec engine.RunSpec) {
	if e.specs != nil {
		_ = e.specs.SaveSpec(name, spec)
	}
}

func (e *Executor) deleteSpec(name string) {
	if e.specs != nil {
		_ = e.specs.DeleteSpec(name)
	}
}

// rollback re-creates the previous container from the snapshot and starts it. If
// that ALSO fails the service is genuinely down — a critical, explicit result.
func (e *Executor) rollback(ctx context.Context, rem ServiceRemediation, snapshot engine.RunSpec, why string) ServiceRemediation {
	if _, err := e.eng.RunContainer(ctx, snapshot); err != nil {
		rem.Outcome = OutcomeFailedDown
		rem.Message = why + " — AND rollback failed to re-create the previous container: " + err.Error()
		return rem
	}
	if err := e.eng.StartContainer(ctx, snapshot.Name); err != nil {
		rem.Outcome = OutcomeFailedDown
		rem.Message = why + " — rollback re-created the previous container but it failed to start: " + err.Error()
		return rem
	}
	rem.Outcome = OutcomeRolledBack
	rem.Message = "recreate failed — restored previous (" + why + ")"
	return rem
}

// specFromContainer reconstructs a RunSpec from an OBSERVED container — the
// rollback source. The container object (ls/inspect) carries image, command, env,
// labels, network membership, resources, caps, read-only, init, volumes (mounts)
// and ports, which is enough to re-create an equivalent container. Best-effort by
// nature (it can't distinguish an image-default entrypoint from a user override,
// so --entrypoint/--shm-size/--tmpfs are not reconstructed); used only on the rare
// failure path, surfaced loudly.
func specFromContainer(c engine.Container) engine.RunSpec {
	cfg := c.Configuration
	spec := engine.RunSpec{
		Name:     c.ID,
		Image:    cfg.Image.Reference,
		Command:  append([]string(nil), cfg.InitProcess.Arguments...),
		Labels:   copyStringMap(cfg.Labels),
		WorkDir:  cfg.InitProcess.WorkingDirectory,
		CPUs:     cfg.Resources.CPUs,
		CapAdd:   append([]string(nil), cfg.CapAdd...),
		CapDrop:  append([]string(nil), cfg.CapDrop...),
		ReadOnly: cfg.ReadOnly,
		Init:     cfg.UseInit,
	}
	if len(cfg.Networks) > 0 {
		spec.Network = cfg.Networks[0].Network
	}
	if env := cfg.InitProcess.Environment; len(env) > 0 {
		spec.Env = map[string]string{}
		for _, kv := range env {
			if i := strings.IndexByte(kv, '='); i > 0 {
				spec.Env[kv[:i]] = kv[i+1:]
			}
		}
	}
	if uid := cfg.InitProcess.User.ID.UID; uid != 0 {
		spec.User = strconv.Itoa(uid)
	}
	if b := cfg.Resources.MemoryInBytes; b > 0 {
		spec.Memory = strconv.FormatInt(b/(1024*1024), 10) + "m"
	}
	for _, m := range cfg.Mounts {
		if name := m.VolumeName(); name != "" {
			spec.Volumes = append(spec.Volumes, engine.RunVolume{Source: name, Target: m.Destination})
		} else if m.IsBind() {
			spec.Volumes = append(spec.Volumes, engine.RunVolume{Source: m.HostPath(), Target: m.Destination})
		}
	}
	for _, p := range cfg.PublishedPorts {
		spec.Ports = append(spec.Ports, engine.RunPort{HostPort: p.HostPort, ContainerPort: p.ContainerPort, Proto: p.Proto})
	}
	return spec
}

func copyStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
