package stacks

import (
	"strings"
	"testing"
)

func findService(s Stack, name string) (Service, bool) {
	for _, svc := range s.Services {
		if svc.Name == name {
			return svc, true
		}
	}
	return Service{}, false
}

func reportHasError(rep ValidationReport, substr string) bool {
	for _, e := range rep.Errors {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

func reportRejects(rep ValidationReport, pathSubstr string) bool {
	for _, r := range rep.Rejected {
		if strings.Contains(r.Path, pathSubstr) {
			return true
		}
	}
	return false
}

func TestParseValidMultiService(t *testing.T) {
	yml := `
name: shop
services:
  web:
    image: docker.io/library/nginx
    ports:
      - "8080:80"
    environment:
      - FOO=bar
      - BARE
    depends_on:
      - api
    restart: always
  api:
    image: docker.io/library/node
    command: node server.js
    environment:
      LEVEL: info
      PORT: 3000
    volumes:
      - data:/var/lib/data
    labels:
      team: core
volumes:
  data: {}
`
	stack, rep := Parse("shop", []byte(yml))
	if !rep.Valid {
		t.Fatalf("expected valid, got errors=%v rejected=%v", rep.Errors, rep.Rejected)
	}
	if len(stack.Services) != 2 {
		t.Fatalf("want 2 services, got %d", len(stack.Services))
	}
	// Services are sorted by name: api before web.
	if stack.Services[0].Name != "api" || stack.Services[1].Name != "web" {
		t.Fatalf("services not sorted: %v", []string{stack.Services[0].Name, stack.Services[1].Name})
	}

	web, _ := findService(stack, "web")
	if web.Image != "docker.io/library/nginx" {
		t.Errorf("web image = %q", web.Image)
	}
	if len(web.Ports) != 1 || web.Ports[0].HostPort != 8080 || web.Ports[0].ContainerPort != 80 || web.Ports[0].Proto != "tcp" {
		t.Errorf("web ports = %+v", web.Ports)
	}
	if web.Environment["FOO"] != "bar" {
		t.Errorf("web FOO = %q", web.Environment["FOO"])
	}
	if _, ok := web.Environment["BARE"]; !ok {
		t.Errorf("bare env key BARE should be present (empty value)")
	}
	if len(web.DependsOn) != 1 || web.DependsOn[0] != "api" {
		t.Errorf("web depends_on = %v", web.DependsOn)
	}
	if web.Restart != RestartAlways {
		t.Errorf("web restart = %q", web.Restart)
	}

	api, _ := findService(stack, "api")
	if len(api.Command) != 2 || api.Command[0] != "node" || api.Command[1] != "server.js" {
		t.Errorf("api command = %v", api.Command)
	}
	// Map-form environment with numeric value.
	if api.Environment["LEVEL"] != "info" || api.Environment["PORT"] != "3000" {
		t.Errorf("api env = %v", api.Environment)
	}
	if len(api.Volumes) != 1 || api.Volumes[0].Source != "data" || api.Volumes[0].Target != "/var/lib/data" {
		t.Errorf("api volumes = %+v", api.Volumes)
	}
	if api.Labels["team"] != "core" {
		t.Errorf("api labels = %v", api.Labels)
	}
	if len(stack.Volumes) != 1 || stack.Volumes[0] != "data" {
		t.Errorf("top-level volumes = %v", stack.Volumes)
	}
}

func TestParseRejectsUnsupportedKeys(t *testing.T) {
	yml := `
services:
  web:
    image: nginx
    build: .
    profiles: ["x"]
    deploy:
      replicas: 3
secrets:
  db_pw:
    file: ./pw
`
	_, rep := Parse("s", []byte(yml))
	if rep.Valid {
		t.Fatal("expected invalid due to rejected keys")
	}
	for _, want := range []string{"services.web.build", "services.web.profiles", "services.web.deploy"} {
		if !reportRejects(rep, want) {
			t.Errorf("expected rejection of %q; rejected=%v", want, rep.Rejected)
		}
	}
	if !reportRejects(rep, "secrets") {
		t.Errorf("expected top-level secrets rejected; rejected=%v", rep.Rejected)
	}
}

func TestParseRejectsOnFailureRestart(t *testing.T) {
	yml := `
services:
  web:
    image: nginx
    restart: on-failure
`
	_, rep := Parse("s", []byte(yml))
	if rep.Valid {
		t.Fatal("expected invalid")
	}
	if !reportHasError(rep, "on-failure") {
		t.Errorf("expected on-failure error; errors=%v", rep.Errors)
	}
}

func TestParseRejectsBadRestart(t *testing.T) {
	yml := `
services:
  web:
    image: nginx
    restart: sometimes
`
	_, rep := Parse("s", []byte(yml))
	if rep.Valid || !reportHasError(rep, "valid policy") {
		t.Errorf("expected invalid restart error; errors=%v", rep.Errors)
	}
}

func TestParseDependsOnCycle(t *testing.T) {
	yml := `
services:
  a:
    image: x
    depends_on: [b]
  b:
    image: x
    depends_on: [c]
  c:
    image: x
    depends_on: [a]
`
	_, rep := Parse("s", []byte(yml))
	if rep.Valid {
		t.Fatal("expected invalid due to cycle")
	}
	if !reportHasError(rep, "cycle") {
		t.Errorf("expected cycle error; errors=%v", rep.Errors)
	}
}

func TestParseDependsOnUnknown(t *testing.T) {
	yml := `
services:
  a:
    image: x
    depends_on: [ghost]
`
	_, rep := Parse("s", []byte(yml))
	if rep.Valid || !reportHasError(rep, "unknown service") {
		t.Errorf("expected unknown-service error; errors=%v", rep.Errors)
	}
}

func TestParseDependsOnLongForm(t *testing.T) {
	yml := `
services:
  web:
    image: nginx
    depends_on:
      api:
        condition: service_started
  api:
    image: node
`
	stack, rep := Parse("s", []byte(yml))
	if !rep.Valid {
		t.Fatalf("expected valid, errors=%v", rep.Errors)
	}
	web, _ := findService(stack, "web")
	if len(web.DependsOn) != 1 || web.DependsOn[0] != "api" {
		t.Errorf("long-form depends_on = %v", web.DependsOn)
	}
}

func TestParseQuotedCommand(t *testing.T) {
	yml := `
services:
  writer:
    image: alpine
    command: sh -c "echo hi > /data/f; sleep 300"
`
	stack, rep := Parse("s", []byte(yml))
	if !rep.Valid {
		t.Fatalf("errors=%v", rep.Errors)
	}
	w, _ := findService(stack, "writer")
	want := []string{"sh", "-c", "echo hi > /data/f; sleep 300"}
	if len(w.Command) != 3 || w.Command[0] != want[0] || w.Command[1] != want[1] || w.Command[2] != want[2] {
		t.Errorf("command = %#v, want %#v", w.Command, want)
	}
}

func TestParseUnbalancedQuotedCommand(t *testing.T) {
	yml := `
services:
  w:
    image: alpine
    command: sh -c "oops
`
	_, rep := Parse("s", []byte(yml))
	if rep.Valid || !reportHasError(rep, "unbalanced quotes") {
		t.Errorf("expected unbalanced-quotes error; errors=%v", rep.Errors)
	}
}

func TestParseMalformedPorts(t *testing.T) {
	for _, bad := range []string{`["abc:80"]`, `["70000:80"]`, `["80:def"]`} {
		yml := "services:\n  web:\n    image: nginx\n    ports: " + bad + "\n"
		_, rep := Parse("s", []byte(yml))
		if rep.Valid {
			t.Errorf("ports %s should be invalid", bad)
		}
	}
}

func TestParseUdpPort(t *testing.T) {
	yml := `
services:
  dns:
    image: x
    ports: ["53:53/udp"]
`
	stack, rep := Parse("s", []byte(yml))
	if !rep.Valid {
		t.Fatalf("expected valid, errors=%v", rep.Errors)
	}
	dns, _ := findService(stack, "dns")
	if dns.Ports[0].Proto != "udp" {
		t.Errorf("proto = %q", dns.Ports[0].Proto)
	}
}

func TestParseMalformedVolume(t *testing.T) {
	yml := `
services:
  web:
    image: nginx
    volumes: ["justonepart"]
`
	_, rep := Parse("s", []byte(yml))
	if rep.Valid || !reportHasError(rep, "volume") {
		t.Errorf("expected volume error; errors=%v", rep.Errors)
	}
}

func TestParseMissingImage(t *testing.T) {
	yml := `
services:
  web:
    command: sleep 1
`
	_, rep := Parse("s", []byte(yml))
	if rep.Valid || !reportHasError(rep, "image is required") {
		t.Errorf("expected image-required error; errors=%v", rep.Errors)
	}
}

func TestParseEmptyAndNoServices(t *testing.T) {
	if _, rep := Parse("s", []byte("")); rep.Valid {
		t.Error("empty doc should be invalid")
	}
	if _, rep := Parse("s", []byte("version: \"3\"\n")); rep.Valid || !reportHasError(rep, "no services") {
		t.Errorf("expected no-services error")
	}
}

func TestParseYAMLSyntaxError(t *testing.T) {
	if _, rep := Parse("s", []byte("services: [unclosed")); rep.Valid {
		t.Error("malformed yaml should be invalid")
	}
}

func TestParseUnknownKeyWarns(t *testing.T) {
	yml := `
services:
  web:
    image: nginx
    boguskey: 1
mystery: true
`
	_, rep := Parse("s", []byte(yml))
	// Unknown keys warn but do not invalidate.
	if !rep.Valid {
		t.Fatalf("unknown keys should warn, not invalidate; errors=%v rejected=%v", rep.Errors, rep.Rejected)
	}
	if len(rep.Warnings) < 2 {
		t.Errorf("expected warnings for unknown keys; warnings=%v", rep.Warnings)
	}
}
