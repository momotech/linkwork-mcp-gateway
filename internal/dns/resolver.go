package dns

import (
	"context"
	"net"
	"sync"
	"time"
)

type NetworkZone string

const (
	ZoneInternal NetworkZone = "internal"
	ZoneOffice   NetworkZone = "office"
	ZoneExternal NetworkZone = "external"

	VirtualK8sNodeHost = "k8s-node.robot.internal"
)

type ResolverConfig struct {
	InternalDNS  string
	OfficeDNS    string
	VirtualHosts map[string]string // hostname -> IP, e.g. "k8s-node.example.internal" -> "127.0.0.1"
}

type ZonedResolver struct {
	internal     *net.Resolver
	office       *net.Resolver
	external     *net.Resolver
	cache        sync.Map // host -> cachedEntry
	virtualHosts map[string]string
}

type cachedEntry struct {
	addrs     []string
	expiresAt time.Time
}

const dnsCacheTTL = 60 * time.Second

func NewZonedResolver(cfg ResolverConfig) *ZonedResolver {
	zr := &ZonedResolver{
		external:     net.DefaultResolver,
		virtualHosts: cfg.VirtualHosts,
	}
	if zr.virtualHosts == nil {
		zr.virtualHosts = make(map[string]string)
	}
	if cfg.InternalDNS != "" {
		zr.internal = newResolver(cfg.InternalDNS)
	} else {
		zr.internal = net.DefaultResolver
	}
	if cfg.OfficeDNS != "" {
		zr.office = newResolver(cfg.OfficeDNS)
	} else {
		zr.office = net.DefaultResolver
	}
	return zr
}

func newResolver(dnsAddr string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "udp", dnsAddr)
		},
	}
}

func (zr *ZonedResolver) Resolve(ctx context.Context, host string, zone NetworkZone) ([]string, error) {
	if ip, ok := zr.virtualHosts[host]; ok {
		return []string{ip}, nil
	}

	cacheKey := string(zone) + ":" + host
	if v, ok := zr.cache.Load(cacheKey); ok {
		entry := v.(*cachedEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.addrs, nil
		}
		zr.cache.Delete(cacheKey)
	}

	r := zr.resolverForZone(zone)
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}

	zr.cache.Store(cacheKey, &cachedEntry{
		addrs:     addrs,
		expiresAt: time.Now().Add(dnsCacheTTL),
	})
	return addrs, nil
}

func (zr *ZonedResolver) resolverForZone(zone NetworkZone) *net.Resolver {
	switch zone {
	case ZoneInternal:
		return zr.internal
	case ZoneOffice:
		return zr.office
	default:
		return zr.external
	}
}

func (zr *ZonedResolver) DialContext(zone NetworkZone) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return net.DialTimeout(network, addr, 10*time.Second)
		}

		if ip := net.ParseIP(host); ip != nil {
			return net.DialTimeout(network, addr, 10*time.Second)
		}

		addrs, err := zr.Resolve(ctx, host, zone)
		if err != nil {
			return nil, err
		}
		if len(addrs) == 0 {
			return nil, &net.DNSError{Err: "no addresses found", Name: host}
		}

		var lastErr error
		for _, a := range addrs {
			conn, dialErr := net.DialTimeout(network, net.JoinHostPort(a, port), 10*time.Second)
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}
