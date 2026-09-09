/*
Copyright 2026.

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

package credentials

import (
	_ "embed"
	"encoding/base64"
	"fmt"
)

//go:embed guest_state.sh
var guestStateScript string

const guestStateUnit = `[Unit]
Description=Verify and mount persistent DBaaS guest state
After=local-fs.target systemd-udev-settle.service dev-disk-by\x2did-virtio\x2ddbaas\x2dstate.device
BindsTo=dev-disk-by\x2did-virtio\x2ddbaas\x2dstate.device
Before=dbaas-executor.service dbaas-agent.service dbaas-redis.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/dbaas-guest-state mount
RemainAfterExit=yes
TimeoutStartSec=60

[Install]
WantedBy=multi-user.target
`

func guestStateCloudInit(p BootstrapParams) (files, commands string) {
	// Every VM requires guest state. The helper rejects missing identity rather
	// than allowing bootstrap to proceed without the persistent volume.
	env := fmt.Sprintf("INSTANCE_UID=%s\nSTATE_PVC_UID=%s\nINITIALIZE_STATE=%t\n", shellSingleQuote(p.InstanceUID), shellSingleQuote(p.GuestStatePVCUID), p.InitializeGuestState)
	for _, f := range []struct{ path, mode, content string }{
		{"/etc/dbaas/guest-state.env", "0600", env},
		{"/usr/local/sbin/dbaas-guest-state", "0700", guestStateScript},
		{"/etc/systemd/system/dbaas-guest-state.service", "0644", guestStateUnit},
	} {
		files += fmt.Sprintf("  - path: %s\n    permissions: %q\n    encoding: b64\n    content: %s\n", f.path, f.mode, base64.StdEncoding.EncodeToString([]byte(f.content)))
	}
	// Run inside bootstrap.sh's set -e boundary; separate runcmd entries would
	// let PostgreSQL bootstrap continue after a failed state-disk check.
	commands = "      /usr/local/sbin/dbaas-guest-state initialize\n      systemctl daemon-reload\n      systemctl enable --now dbaas-guest-state.service\n"
	return files, commands
}
