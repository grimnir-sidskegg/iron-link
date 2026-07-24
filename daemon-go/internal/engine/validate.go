// Static node-config validation: construct the node's outbound in its core
// WITHOUT dialing. The core's own loader enforces the protocol's param rules —
// a Shadowsocks method/key-size mismatch ("bad key length, required 32, got
// 28"), an unknown cipher, a Reality block missing pbk/sid, a malformed uuid —
// so a broken node surfaces a PRECISE reason at the diagnosis's config stage
// instead of a vague network failure three stages later.

package engine

import (
	"bytes"
	"encoding/json"

	xcore "github.com/xtls/xray-core/core"
	xserial "github.com/xtls/xray-core/infra/conf/serial"

	"ironlink/daemon/internal/api"
	"ironlink/daemon/internal/proxy"
)

// ValidateNodeConfig constructs the node's outbound for core without opening any
// socket and returns the core's loader error, or nil when the config is
// structurally valid. Native nodes construct through sing-box's box.New (which
// validates ciphers, key sizes, Reality params, …); xhttp nodes through xray's
// instance constructor. Nothing is dialed — this is pure config sanity.
func ValidateNodeConfig(p proxy.Profile, core api.CoreType) error {
	if core == api.CoreXray {
		cfg, err := CompileXrayClient(p, "@il-validate")
		if err != nil {
			return err
		}
		c, err := xserial.LoadJSONConfig(bytes.NewReader(cfg))
		if err != nil {
			return err
		}
		inst, err := xcore.New(c) // construct only; never Start (no sockets opened)
		if err != nil {
			return err
		}
		return inst.Close()
	}

	ob, err := p.SingBoxOutbound("validate")
	if err != nil {
		return err
	}
	cfg, err := json.Marshal(map[string]any{
		"log":       map[string]any{"disabled": true},
		"outbounds": []any{ob},
	})
	if err != nil {
		return err
	}
	b, err := BuildBox(cfg, nil, nil) // box.New constructs (and validates) every outbound
	if b != nil {
		b.Close()
	}
	return err
}
