package compose

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yaml")
	os.WriteFile(path, []byte(`
services:
  proxy:
    image: caddy:2
    ports: ["80:80", {target: 443, published: 8443, protocol: tcp}]
    networks: [front, edge]
    depends_on: [app]
  app:
    build: .
    volumes: ["./conf:/etc/app:ro", "data:/var/lib/app", {type: tmpfs, target: /tmp}]
    networks:
      front:
      back: {aliases: [api]}
    depends_on:
      db: {condition: service_healthy}
    healthcheck: {test: ["CMD", "true"]}
    deploy: {replicas: 2}
  db:
    image: postgres:17
volumes:
  data:
networks:
  front:
  back:
  edge: {external: true}
`), 0o644)

	p, err := Load(path, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Volumes, []string{"shop_data"}) || !reflect.DeepEqual(p.Networks, []string{"shop_back", "edge", "shop_front"}) {
		t.Errorf("top level = %+v", p)
	}
	app, db, proxy := p.Services[0], p.Services[1], p.Services[2]
	if app.Name != "app" || !app.Build || !app.Healthcheck || app.Replicas != 2 ||
		!reflect.DeepEqual(app.DependsOn, []string{"db"}) || !reflect.DeepEqual(app.Networks, []string{"shop_back", "shop_front"}) {
		t.Errorf("app = %+v", app)
	}
	wantVols := []Volume{{"bind", "./conf", "/etc/app"}, {"volume", "shop_data", "/var/lib/app"}, {"tmpfs", "", "/tmp"}}
	if !reflect.DeepEqual(app.Volumes, wantVols) {
		t.Errorf("volumes = %+v", app.Volumes)
	}
	if !reflect.DeepEqual(db.Networks, []string{"shop_default"}) {
		t.Errorf("db networks = %v", db.Networks)
	}
	if !reflect.DeepEqual(proxy.Ports, []string{"80:80", "8443:443"}) || !reflect.DeepEqual(proxy.DependsOn, []string{"app"}) || !reflect.DeepEqual(proxy.Networks, []string{"edge", "shop_front"}) {
		t.Errorf("proxy = %+v", proxy)
	}
}
