// Package guide holds the things kvmx cannot do from inside a guest or
// container: provider-side toggles and container launch flags. It prints exact
// commands for the human who controls the layer below.
package guide

import (
	"fmt"
	"strings"

	"github.com/KVM-X/KVM-X/internal/detect"
)

// Action is a manual step for the person who controls the layer below.
type Action struct {
	Title string   `json:"title"`
	Steps []string `json:"steps"`
	URL   string   `json:"url,omitempty"`
}

// Manual returns the manual actions that apply to the report's blockers.
func Manual(r *detect.Report) []Action {
	var out []Action
	for _, b := range r.Blockers {
		switch b.ID {
		case detect.BCPUNoVirt:
			out = append(out, forProvider(r)...)
		case detect.BContainerNoDev, detect.BContainerDenied:
			out = append(out, containerFlags(r)...)
		case detect.BKernelNoKVM, detect.BModuleNotLoaded:
			if r.Virt.IsContainer {
				out = append(out, Action{Title: "Fix KVM on the container host (containers share its kernel)",
					Steps: []string{"$ sudo modprobe " + r.KVM.Module, "$ ls -l /dev/kvm   # must exist on the host", "then relaunch the container with the KVM device: kvmx flags docker"}})
			}
		case detect.BKVMBroken:
			out = append(out, Action{Title: "/dev/kvm exists but does not work",
				Steps: []string{"$ dmesg | grep -i -E 'kvm|vmx|svm'", "Look for 'disabled by bios' or 'hardware support not enabled'; enable virtualization in firmware or expose it from the hypervisor."}})
		}
	}
	return dedupe(out)
}

func dedupe(in []Action) []Action {
	seen := map[string]bool{}
	var out []Action
	for _, a := range in {
		if !seen[a.Title] {
			seen[a.Title] = true
			out = append(out, a)
		}
	}
	return out
}

func forProvider(r *detect.Report) []Action {
	instance := r.Provider.InstanceType
	switch r.Provider.ID {
	case "aws":
		steps := []string{
			"Nested virtualization on non-metal EC2 needs a supported type (8th-gen Intel C8i/M8i/R8i families and some 7th-gen Intel types per AWS docs) and must be enabled per instance. Graviton (arm64) VMs cannot; use a .metal type.",
			"$ aws ec2 describe-instance-types --filters Name=processor-info.supported-features,Values=nested-virtualization --query 'InstanceTypes[].InstanceType' --output text",
			"Launch:   $ aws ec2 run-instances --instance-type r8i.large --cpu-options NestedVirtualization=enabled ...",
			"Existing: stop the instance, then",
			"          $ aws ec2 modify-instance-cpu-options --instance-id i-XXXX --core-count N --threads-per-core T --nested-virtualization enabled",
			"Or use any *.metal instance type, where KVM works natively.",
		}
		if instance != "" {
			steps = append([]string{"Detected instance type: " + instance}, steps...)
		}
		return []Action{{Title: "AWS EC2: turn on nested virtualization", Steps: steps,
			URL: "https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/amazon-ec2-nested-virtualization.html"}}
	case "gcp":
		return []Action{{Title: "Google Compute Engine: enable nested virtualization", Steps: []string{
			"Supported only on Intel VMs (and N4D); not on E2, memory-optimized, or Arm VMs. Linux KVM only.",
			"$ gcloud compute instances create NAME --enable-nested-virtualization --min-cpu-platform=\"Intel Haswell\" ...",
			"Existing disk: create an image carrying the nested-virt license, then boot a new VM from it:",
			"$ gcloud compute images create nested-img --source-disk DISK --source-disk-zone ZONE --licenses https://www.googleapis.com/compute/v1/projects/vm-options/global/licenses/enable-vmx",
			"If the flag is rejected, check the org policy constraints/compute.disableNestedVirtualization.",
		}, URL: "https://cloud.google.com/compute/docs/instances/nested-virtualization/overview"}}
	case "azure":
		return []Action{{Title: "Azure: pick a nested-virtualization capable size", Steps: []string{
			"Dv3/Ev3 and newer general-purpose and memory series (for example Dv4/Dv5, Ev4/Ev5) support nested virtualization; B-series and A-series do not. Verify your size in Microsoft's size documentation.",
			"$ grep -c -E 'vmx|svm' /proc/cpuinfo   # must be > 0 after resizing",
			"$ az vm resize --resource-group RG --name VM --size Standard_D4s_v5",
		}}}
	case "oracle":
		return []Action{{Title: "Oracle Cloud: use a bare-metal shape", Steps: []string{
			"OCI virtual-machine shapes generally do not expose VT-x/AMD-V. Use a BM.* bare-metal shape, or run kvmx in TCG mode.",
		}}}
	case "hetzner", "digitalocean", "vultr", "linode", "ovh", "scaleway", "alibaba", "openstack", "unknown-vm":
		return []Action{{Title: r.Provider.Name + ": hardware virtualization is not exposed on this plan", Steps: []string{
			"The VM has no vmx/svm CPU flag, so the provider has not enabled nested virtualization for this plan or host.",
			"Ask support whether a plan with nested virtualization exists, or use a dedicated/bare-metal server where KVM works natively.",
		}}}
	case "qemu":
		return []Action{{Title: "QEMU/KVM host (Proxmox, libvirt, OpenStack, oVirt): expose VT-x/AMD-V to this guest", Steps: []string{
			"On the host, nested must be on: $ cat /sys/module/kvm_intel/parameters/nested   # (or kvm_amd) expect Y or 1",
			"If not: $ echo 'options kvm_intel nested=1' | sudo tee /etc/modprobe.d/kvm-nested.conf   # kvm_amd for AMD; then reload the module / reboot",
			"Proxmox:   $ qm set VMID --cpu host          (CPU type 'host'; generic kvm64/qemu64 hides vmx/svm)",
			"libvirt:   <cpu mode='host-passthrough' check='none'/>   (virt-manager: Copy host CPU configuration)",
			"OpenStack: nova.conf [libvirt] cpu_mode = host-passthrough",
			"Plain QEMU: -cpu host   (add +vmx or +svm if using a named model)",
		}}}
	case "vmware":
		return []Action{{Title: "VMware: expose hardware-assisted virtualization", Steps: []string{
			"VM Settings > Processors > 'Virtualize Intel VT-x/EPT or AMD-V/RVI', or add to the .vmx: vhv.enable = \"TRUE\"",
		}}}
	case "hyperv":
		return []Action{{Title: "Hyper-V: expose virtualization extensions", Steps: []string{
			"With the VM powered off, in an elevated PowerShell on the host:",
			"$ Set-VMProcessor -VMName <vm> -ExposeVirtualizationExtensions $true",
		}}}
	case "virtualbox":
		return []Action{{Title: "VirtualBox: enable nested VT-x/AMD-V", Steps: []string{
			"With the VM powered off: $ VBoxManage modifyvm <vm> --nested-hw-virt on",
		}}}
	case "xen":
		return []Action{{Title: "Xen: nested HVM", Steps: []string{
			"Only HVM guests can use it: in the domU config set nestedhvm=1 (and cpuid exposure of vmx/svm). PV guests (older EC2) cannot.",
		}}}
	case "bare-metal":
		return []Action{{Title: "Enable virtualization in firmware", Steps: []string{
			"Reboot into BIOS/UEFI and enable Intel Virtualization Technology (VT-x) or AMD SVM Mode, then reboot.",
			"$ dmesg | grep -i -E 'kvm|vmx|svm'   # 'disabled by bios' confirms it",
		}}}
	}
	if r.Virt.ContainerType == "wsl" {
		return []Action{{Title: "WSL2: enable nested virtualization", Steps: []string{
			"In %UserProfile%\\.wslconfig add:  [wsl2]  nestedVirtualization=true   then run: wsl --shutdown",
		}}}
	}
	return nil
}

func containerFlags(r *detect.Report) []Action {
	return []Action{{Title: "Pass /dev/kvm into the container", Steps: strings.Split(Flags("docker", r), "\n")}}
}

// Flags renders launch flags for a runtime: docker, podman, lxc, lxd, k8s.
func Flags(kind string, r *detect.Report) string {
	minor := 232
	if r != nil && r.KVM.MiscMinor > 0 {
		minor = r.KVM.MiscMinor
	}
	switch kind {
	case "docker":
		return fmt.Sprintf(`# least privilege (recommended): device + its group
docker run --device /dev/kvm --group-add "$(stat -c %%g /dev/kvm)" IMAGE
# if the group id cannot be mapped, or the container must create the node itself:
docker run --device-cgroup-rule='c 10:%d rwm' --cap-add MKNOD IMAGE
# last resort:
docker run --privileged IMAGE
# docker-compose.yml
#   services:
#     kvmx:
#       devices: ["/dev/kvm:/dev/kvm"]
#       group_add: ["kvm"]`, minor)
	case "podman":
		return "podman run --device /dev/kvm --group-add keep-groups IMAGE\n# rootless: the user must be in the host 'kvm' group"
	case "lxc":
		return fmt.Sprintf(`# /var/lib/lxc/NAME/config  (LXC 4+ / cgroup v2)
lxc.cgroup2.devices.allow = c 10:%d rwm
lxc.mount.entry = /dev/kvm dev/kvm none bind,optional,create=file
# cgroup v1 hosts use: lxc.cgroup.devices.allow = c 10:%d rwm`, minor, minor)
	case "lxd":
		return "lxc config device add NAME kvm unix-char source=/dev/kvm path=/dev/kvm\n# unprivileged containers also need: lxc config set NAME security.nesting true"
	case "k8s":
		return `# preferred: a KVM device plugin (for example KubeVirt's) exposing devices.kubevirt.io/kvm
resources:
  limits:
    devices.kubevirt.io/kvm: "1"
# fallback: hostPath + privileged
volumeMounts: [{name: kvm, mountPath: /dev/kvm}]
volumes: [{name: kvm, hostPath: {path: /dev/kvm, type: CharDevice}}]
securityContext: {privileged: true}`
	}
	return "unknown runtime " + kind + "; use one of: docker podman lxc lxd k8s"
}
