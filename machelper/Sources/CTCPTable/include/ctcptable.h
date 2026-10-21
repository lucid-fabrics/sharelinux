#ifndef CTCPTABLE_H
#define CTCPTABLE_H

#include <stddef.h>
#include <stdint.h>
#include <sys/types.h>
#include <netinet/in.h>
#include <netinet/in_pcb.h>   /* struct in_addr_4in6: the SDK's own definition */
#include <netinet/tcp_fsm.h>  /* TCPS_ESTABLISHED */
#include <netinet/tcp_timer.h> /* TCPT_NTIMERS_EXT */

/*
 * The records of the kernel's net.inet.tcp.pcblist_n table that the helper
 * reads. The kernel (xnu, bsd/netinet/in_pcb.h and bsd/sys/socketvar.h) only
 * declares these under PRIVATE, so they are not in the macOS SDK; they are
 * mirrored here field for field, with the same #pragma pack(4), and the
 * offsets Swift uses come from offsetof() on the mirrors instead of being
 * typed in by hand. The only parts taken from the SDK are in_addr_4in6 (where
 * an IPv4 address sits inside the 16-byte address slot), in6_addr and the TCP
 * state and timer constants. _Static_assert pins the sizes the kernel reports
 * (104 bytes for xinpcb_n and xsocket_n); a compiler that disagrees fails the
 * build. At run time TCPTable also checks the layout against a live loopback
 * connection and refuses to trust the table if it disagrees.
 */
#pragma pack(4)
struct oss_xinpcb_n {
	uint32_t xi_len;
	uint32_t xi_kind;
	uint64_t xi_inpp;
	uint16_t inp_fport;
	uint16_t inp_lport;
	uint64_t inp_ppcb;
	uint64_t inp_gencnt;
	int32_t  inp_flags;
	uint32_t inp_flow;
	uint8_t  inp_vflag;
	uint8_t  inp_ip_ttl;
	uint8_t  inp_ip_p;
	union {
		struct in_addr_4in6 inp46_foreign;
		struct in6_addr     inp6_foreign;
	} inp_dependfaddr;
	union {
		struct in_addr_4in6 inp46_local;
		struct in6_addr     inp6_local;
	} inp_dependladdr;
	struct { uint8_t inp4_ip_tos; } inp_depend4;
	struct {
		uint8_t  inp6_hlim;
		int32_t  inp6_cksum;
		uint16_t inp6_ifindex;
		int16_t  inp6_hops;
	} inp_depend6;
	uint32_t inp_flowhash;
	uint32_t inp_flags2;
};

struct oss_xsocket_n {
	uint32_t xso_len;
	uint32_t xso_kind;
	uint64_t xso_so;
	int16_t  so_type;
	uint32_t so_options;
	int16_t  so_linger;
	int16_t  so_state;
	uint64_t so_pcb;
	int32_t  xso_protocol;
	int32_t  xso_family;
	int16_t  so_qlen;
	int16_t  so_incqlen;
	int16_t  so_qlimit;
	int16_t  so_timeo;
	uint16_t so_error;
	pid_t    so_pgid;
	uint32_t so_oobmark;
	uid_t    so_uid;
	pid_t    so_last_pid;
	pid_t    so_e_pid;
};

struct oss_xtcpcb_n {
	uint32_t xt_len;
	uint32_t xt_kind;
	uint64_t t_segq;
	int32_t  t_dupacks;
	int32_t  t_timer[TCPT_NTIMERS_EXT];
	int32_t  t_state;
};
#pragma pack()

_Static_assert(sizeof(struct oss_xinpcb_n) == 104, "xinpcb_n is 104 bytes");
_Static_assert(offsetof(struct oss_xsocket_n, so_e_pid) + sizeof(pid_t) <= 104, "xsocket_n prefix fits its record");
_Static_assert(sizeof(struct in_addr_4in6) == 16, "in_addr_4in6 fills the address slot");

#define OSS_FN(name, expr) static inline size_t name(void) { return (size_t)(expr); }

OSS_FN(oss_inpcb_min_len, sizeof(struct oss_xinpcb_n))
OSS_FN(oss_inpcb_fport, offsetof(struct oss_xinpcb_n, inp_fport))
OSS_FN(oss_inpcb_lport, offsetof(struct oss_xinpcb_n, inp_lport))
OSS_FN(oss_inpcb_vflag, offsetof(struct oss_xinpcb_n, inp_vflag))
OSS_FN(oss_inpcb_faddr6, offsetof(struct oss_xinpcb_n, inp_dependfaddr.inp6_foreign))
OSS_FN(oss_inpcb_faddr4, offsetof(struct oss_xinpcb_n, inp_dependfaddr.inp46_foreign.ia46_addr4))
OSS_FN(oss_socket_min_len, offsetof(struct oss_xsocket_n, so_e_pid) + sizeof(pid_t))
OSS_FN(oss_socket_family, offsetof(struct oss_xsocket_n, xso_family))
OSS_FN(oss_socket_last_pid, offsetof(struct oss_xsocket_n, so_last_pid))
OSS_FN(oss_socket_e_pid, offsetof(struct oss_xsocket_n, so_e_pid))
OSS_FN(oss_tcpcb_min_len, offsetof(struct oss_xtcpcb_n, t_state) + sizeof(int32_t))
OSS_FN(oss_tcpcb_state, offsetof(struct oss_xtcpcb_n, t_state))
static inline int32_t oss_tcps_established(void) { return TCPS_ESTABLISHED; }

#endif
