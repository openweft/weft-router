module github.com/openweft/weft-router

go 1.26

require (
	// Prometheus : /metrics scrape, sur un port séparé du data plane
	// pour que le scrape surface ne partage pas le fate (un handler hang
	// ne peut pas stall les routes BGP).
	github.com/prometheus/client_golang v1.20.5

	// Cobra : convention CLI openweft (jamais flag stdlib).
	github.com/spf13/cobra v1.8.1

// À ajouter quand on remplit les TODO du scaffold :
//   github.com/osrg/gobgp/v3       — moteur BGP-4 + EVPN + flowspec
//   github.com/vishvananda/netlink — programmation FIB Linux
//   github.com/nats-io/nats.go     — transport de config dynamique
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.55.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	golang.org/x/sys v0.25.0 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)
