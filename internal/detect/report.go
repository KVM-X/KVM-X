// Package detect inspects a machine and decides which rung of the kvmx
// enablement ladder it is on and what stands between it and a better one.
package detect

import "github.com/KVM-X/KVM-X/internal/sys"

// SchemaVersion of the JSON report; bump on breaking changes.
const SchemaVersion = 1

// Mode is the final status kvmx reports. It is never inflated: software
// emulation is never reported as KVM.
type Mode string

const (
	ModeHardware Mode = "HARDWARE-KVM"
	ModeNested   Mode = "NESTED-KVM"
	ModeTCG      Mode = "SOFTWARE-EMULATION (TCG)"
)

// Code is the stable machine-readable form of a Mode.
func (m Mode) Code() string {
	switch m {
	case ModeHardware:
		return "hardware-kvm"
	case ModeNested:
		return "nested-kvm"
	default:
		return "software-tcg"
	}
}

// Penalty is the honest performance expectation, shown next to the status.
func (m Mode) Penalty() string {
	switch m {
	case ModeHardware:
		return "near-native CPU and memory speed (a few percent overhead)"
	case ModeNested:
		return "CPU-bound work ~5-30% slower than hardware KVM; VM exits, MMIO and disk I/O noticeably slower"
	default:
		return "CPU-bound guests typically ~10x slower than hardware KVM (5-30x by workload); measure with `kvmx bench`"
	}
}

type Report struct {
	Schema    int       `json:"schema_version"`
	Version   string    `json:"kvmx_version"`
	Mode      Mode      `json:"mode"`
	ModeCode  string    `json:"mode_code"`
	Penalty   string    `json:"expected_penalty"`
	Ready     bool      `json:"ready"`
	ReadyNote string    `json:"ready_note,omitempty"`
	Potential Mode      `json:"potential_mode"`
	Blockers  []Blocker `json:"blockers"`
	CPU       CPU       `json:"cpu"`
	Virt      Virt      `json:"virt"`
	Provider  Provider  `json:"provider"`
	KVM       KVM       `json:"kvm"`
	Sandbox   Sandbox   `json:"sandbox"`
	Tools     Tools     `json:"tools"`
	Distro    Distro    `json:"distro"`
	Notes     []string  `json:"notes,omitempty"`
}

// Blocker is one reason the machine is not on a KVM rung.
type Blocker struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
	// Fixable means a kvmx enable step can resolve it (possibly via sudo).
	Fixable bool `json:"fixable"`
}

type CPU struct {
	Arch       string   `json:"arch"` // x86_64, aarch64, ...
	Vendor     string   `json:"vendor,omitempty"`
	Model      string   `json:"model,omitempty"`
	Cores      int      `json:"logical_cpus"`
	VirtExt    string   `json:"virt_extension"` // vmx, svm, none, n/a
	Hypervisor bool     `json:"hypervisor_flag"`
	VirtFlags  []string `json:"virt_flags,omitempty"`
	GenericVM  bool     `json:"generic_vm_cpu_model,omitempty"`
}

type Virt struct {
	IsVM          bool     `json:"is_vm"`
	VMType        string   `json:"vm_type,omitempty"`
	IsContainer   bool     `json:"is_container"`
	ContainerType string   `json:"container_type,omitempty"`
	Evidence      []string `json:"evidence,omitempty"`
}

type Provider struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Product      string   `json:"product,omitempty"`
	Vendor       string   `json:"vendor,omitempty"`
	InstanceType string   `json:"instance_type,omitempty"`
	Evidence     []string `json:"evidence,omitempty"`
}

type KVM struct {
	Module          string       `json:"module,omitempty"` // kvm_intel, kvm_amd, kvm (arm64 builtin)
	ModuleLoaded    bool         `json:"module_loaded"`
	ModuleBuiltin   bool         `json:"module_builtin"`
	ModuleAvailable bool         `json:"module_available"`
	NestedParam     string       `json:"nested_param,omitempty"` // Y, N, ""
	ModuleRefs      int          `json:"module_refcount"`
	DevExists       bool         `json:"dev_kvm_exists"`
	DevMode         string       `json:"dev_kvm_mode,omitempty"`
	DevOwnerGID     uint32       `json:"dev_kvm_gid,omitempty"`
	DevGroup        string       `json:"dev_kvm_group,omitempty"`
	DevIsChar       bool         `json:"dev_kvm_is_char"`
	MiscMinor       int          `json:"misc_minor"` // from /proc/misc, -1 unknown
	KernelConfig    KernelConfig `json:"kernel_config"`
	Probe           sys.KVMProbe `json:"probe"`
	Usable          bool         `json:"usable"`
	InKVMGroupDB    bool         `json:"user_in_kvm_group_db"`
	InKVMGroupProc  bool         `json:"user_in_kvm_group_effective"`
	MSHV            bool         `json:"dev_mshv_exists"`
}

type KernelConfig struct {
	Source string `json:"source,omitempty"`
	KVM    string `json:"kvm,omitempty"`
	Intel  string `json:"kvm_intel,omitempty"`
	AMD    string `json:"kvm_amd,omitempty"`
}

type Sandbox struct {
	UID            int    `json:"uid"`
	User           string `json:"user"`
	CapSysModule   bool   `json:"cap_sys_module"`
	CapMknod       bool   `json:"cap_mknod"`
	CapSysAdmin    bool   `json:"cap_sys_admin"`
	Seccomp        int    `json:"seccomp"`
	NoNewPrivs     bool   `json:"no_new_privs"`
	SysfsReadOnly  bool   `json:"sysfs_read_only"`
	ModulesLocked  bool   `json:"modules_disabled"`
	LibModules     bool   `json:"lib_modules_present"`
	CgroupVersion  int    `json:"cgroup_version"`
	HasSudo        bool   `json:"has_sudo"`
	SudoPasswdless bool   `json:"sudo_passwordless"`
}

type Tools struct {
	QEMU        string `json:"qemu,omitempty"` // path of the real (non-shim) binary for this arch
	QEMUVersion string `json:"qemu_version,omitempty"`
	QEMUImg     string `json:"qemu_img,omitempty"`
	TCG         bool   `json:"qemu_has_tcg"`
	Libvirt     string `json:"virsh,omitempty"`
	Virtiofsd   string `json:"virtiofsd,omitempty"`
	Docker      string `json:"docker,omitempty"`
	Podman      string `json:"podman,omitempty"`
	UML         string `json:"user_mode_linux,omitempty"`
	PkgManager  string `json:"package_manager,omitempty"`
	ShimActive  bool   `json:"kvmx_shim_active"`
}

type Distro struct {
	ID      string `json:"id,omitempty"`
	Version string `json:"version,omitempty"`
	Like    string `json:"id_like,omitempty"`
	Pretty  string `json:"pretty,omitempty"`
}
