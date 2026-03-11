# -*- mode: ruby -*-
# vi: set ft=ruby :

AGENT_PORT = 50051

VMS = {
  "alma8"  => { box: "almalinux/8",  ip: "192.168.200.10" },
  "alma9"  => { box: "almalinux/9",  ip: "192.168.200.11" },
  "alma10" => { box: "almalinux/10", ip: "192.168.200.12" },
}

Vagrant.configure("2") do |config|
  VMS.each do |name, opts|
    config.vm.define name do |node|
      node.vm.box = opts[:box]
      node.vm.hostname = name
      node.vm.network "private_network",
        ip: opts[:ip],
        libvirt__network_name: "alma-net",
        libvirt__dhcp_enabled: false,
        libvirt__forward_mode: "nat"

      node.vm.provider "libvirt" do |lv|
        lv.memory = 2048
        lv.cpus   = 2
        lv.nested = true
        lv.qemu_use_session = false
        lv.management_network_name = "default"
        lv.management_network_address = "192.168.121.0/24"
      end

      # Copy and start the gRPC agent.
      node.vm.provision "file",
        source:      "agent/alma-agent",
        destination: "/tmp/alma-agent"

      node.vm.provision "shell", inline: <<-SHELL
        # Ensure static IP on eth1 (some AlmaLinux versions skip static assignment)
        if ! ip addr show eth1 | grep -q '#{opts[:ip]}'; then
          ip addr add #{opts[:ip]}/24 dev eth1
        fi
        # Make it persistent via NetworkManager
        nmcli con show 2>/dev/null | grep -q 'alma-static' || \
          nmcli con add type ethernet con-name alma-static ifname eth1 \
            ipv4.method manual ipv4.addresses #{opts[:ip]}/24 2>/dev/null || true

        install -m 0755 /tmp/alma-agent /usr/local/bin/alma-agent
        cat > /etc/systemd/system/alma-agent.service <<'UNIT'
[Unit]
Description=Alma gRPC Agent
After=network.target

[Service]
ExecStart=/usr/local/bin/alma-agent --listen :#{AGENT_PORT}
Restart=always

[Install]
WantedBy=multi-user.target
UNIT
        systemctl daemon-reload
        systemctl enable --now alma-agent.service
      SHELL
    end
  end
end
