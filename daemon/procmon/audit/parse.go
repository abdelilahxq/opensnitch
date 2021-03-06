package audit

import (
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/gustavo-iniguez-goya/opensnitch/daemon/log"
)

var (
	newEvent = false
	netEvent = &Event{}

	auditRE, _ = regexp.Compile(`([a-zA-Z0-9\-_]+)=([a-zA-Z0-9:'\-\/\"\.\,_\(\)]+)`)
	rawEvent   = make(map[string]string)
)

const (
	SYSCALL_SOCKET     = "41"
	SYSCALL_CONNECT    = "42"
	SYSCALL_SOCKETPAIR = "53"
	SYSCALL_EXECVE     = "59"

	// /usr/include/x86_64-linux-gnu/bits/socket_type.h
	SOCK_STREAM    = "1"
	SOCK_DGRAM     = "2"
	SOCK_RAW       = "3"
	SOCK_SEQPACKET = "5"
	SOCK_PACKET    = "10"

	// /usr/include/x86_64-linux-gnu/bits/socket.h
	PF_UNSPEC = "0"
	PF_LOCAL  = "1" // PF_UNIX
	PF_INET   = "2"
	PF_INET6  = "10"

	// /etc/protocols
	PROTO_IP  = "0"
	PROTO_TCP = "6"
	PROTO_UDP = "17"
)

// https://access.redhat.com/documentation/en-US/Red_Hat_Enterprise_Linux/7/html/Security_Guide/sec-Audit_Record_Types.html
const (
	AUDIT_TYPE_PROCTITLE = "type=PROCTITLE"
	AUDIT_TYPE_CWD       = "type=CWD"
	AUDIT_TYPE_PATH      = "type=PATH"
	AUDIT_TYPE_EXECVE    = "type=EXECVE"
	AUDIT_TYPE_SOCKADDR  = "type=SOCKADDR"
	AUDIT_TYPE_EOE       = "type=EOE"
)

var (
	SYSCALL_SOCKET_STR     = fmt.Sprint("syscall=", syscall.SYS_SOCKET)
	SYSCALL_CONNECT_STR    = fmt.Sprint("syscall=", syscall.SYS_CONNECT)
	SYSCALL_SOCKETPAIR_STR = fmt.Sprint("syscall=", syscall.SYS_SOCKETPAIR)
	SYSCALL_EXECVE_STR     = fmt.Sprint("syscall=", syscall.SYS_EXECVE)
)

// isFromOurPid checks out if the event has been generated from ourselfs.
func isFromOurPid(pid, ppid string) bool {
	return pid == strconv.Itoa(ourPid) || ppid == strconv.Itoa(ourPid)
}

// parseNetLine parses a SOCKADDR message type of the form:
// saddr string: inet6 host:2001:4860:4860::8888 serv:53
func parseNetLine(line string, decode bool) (family string, dstHost net.IP, dstPort int) {
	if decode == true {
		line = decodeString(line)
	}
	pieces := strings.Split(line, " ")
	family = pieces[0]

	if family[:4] != "inet" {
		return family, dstHost, 0
	}

	if len(pieces) > 1 && pieces[1][:5] == "host:" {
		dstHost = net.ParseIP(strings.Split(pieces[1], "host:")[1])
	}
	if len(pieces) > 2 && pieces[2][:5] == "serv:" {
		_dstPort, err := strconv.Atoi(strings.Split(line, "serv:")[1])
		if err != nil {
			dstPort = -1
		} else {
			dstPort = _dstPort
		}
	}

	return family, dstHost, dstPort
}

// decodeString will try to decode a string encoded in hexadecimal.
// If the string can not be decoded, the original string will be returned.
// In that case, usually it means that it's a non-encoded string.
func decodeString(s string) string {
	if decoded, err := hex.DecodeString(s); err != nil {
		return s
	} else {
		return fmt.Sprintf("%s", decoded)
	}
}

// extractFields parsed an audit raw message, and extracts all the fields.
func extractFields(rawMessage string, newEvent *map[string]string) {
	Lock.Lock()
	defer Lock.Unlock()

	if auditRE == nil {
		newEvent = nil
		return
	}
	fieldList := auditRE.FindAllStringSubmatch(rawMessage, -1)
	if fieldList == nil {
		newEvent = nil
		return
	}
	for _, field := range fieldList {
		(*newEvent)[field[1]] = field[2]
	}
}

// populateEvent populates our Event from a raw parsed message.
func populateEvent(aevent *Event, eventFields *map[string]string) *Event {
	if aevent != nil {
		Lock.Lock()
		defer Lock.Unlock()

		for k, v := range *eventFields {
			switch k {
			case "a0":
				if (*eventFields)["syscall"] == SYSCALL_SOCKET ||
					(*eventFields)["syscall"] == SYSCALL_CONNECT ||
					(*eventFields)["syscall"] == SYSCALL_SOCKETPAIR {
					// XXX: is it wort to intercept PF_LOCAL/PF_FILE as well?
					if v == PF_INET6 || v == "a" {
						aevent.NetFamily = "inet6"
					} else if v == PF_INET || v == PF_LOCAL || v == PF_UNSPEC {
						aevent.NetFamily = "inet"
					}
				}
			case "a1":
				if (*eventFields)["syscall"] == SYSCALL_SOCKET {
					if aevent.NetFamily == "" &&
						(v == "0" || v == SOCK_STREAM || v == SOCK_DGRAM ||
							v == SOCK_RAW || v == SOCK_SEQPACKET || v == SOCK_PACKET) {
						aevent.NetFamily = "inet"
					}
				}
			case "fam":
				aevent.NetFamily = string(v)
			case "lport":
				aevent.DstPort, _ = strconv.Atoi(v)
			case "laddr":
				aevent.DstHost = net.ParseIP(string(v))
			case "saddr":
				// TODO
				/*
					if aevent.NetFamily == "" {
						aevent.NetFamily, aevent.DstHost, aevent.DstPort = parseNetLine(v, true)
					} else {
						_, aevent.DstHost, aevent.DstPort = parseNetLine(v, true)
					}
				*/
			case "exe":
				aevent.ProcPath = strings.Trim(decodeString(v), "\"")
			case "comm":
				aevent.ProcName = strings.Trim(decodeString(v), "\"")
			case "proctitle":
				aevent.ProcCmdLine = strings.Trim(decodeString(v), "\"")
			case "tty":
				aevent.TTY = string(v)
			case "pid":
				aevent.Pid, _ = strconv.Atoi(v)
			case "ppid":
				aevent.PPid, _ = strconv.Atoi(v)
			case "uid":
				aevent.Uid, _ = strconv.Atoi(v)
			case "gid":
				aevent.Gid, _ = strconv.Atoi(v)
			case "success":
				aevent.Success = string(v)
			case "cwd":
				aevent.ProcDir = strings.Trim(decodeString(v), "\"")
			case "inode":
				aevent.INode, _ = strconv.Atoi(v)
			case "dev":
				aevent.Dev = string(v)
			case "mode":
				aevent.ProcMode = string(v)
			case "ouid":
				aevent.OUid, _ = strconv.Atoi(v)
			case "ogid":
				aevent.OGid, _ = strconv.Atoi(v)
			case "syscall":
				aevent.Syscall, _ = strconv.Atoi(v)
			case "exit":
				aevent.Exit, _ = strconv.Atoi(v)
			case "type":
				aevent.EventType = string(v)
			case "msg":
				// TODO
				//aevent.Timestamp = event.Timestamp
				//aevent.Serial = event.Serial
			}
		}
	}

	return aevent
}

// parseEvent parses an auditd event, discards the unwanted ones, and adds
// the ones we're interested in to an array.
// We're only interested in the socket,socketpair,connect and execve syscalls.
// Events from us are excluded.
//
// When we received an event, we parse and add it to the list as soon as we can.
// If the next messages of the set have additional information, we update the
// event.
func parseEvent(rawMessage string, eventChan chan<- Event) {
	aEvent := make(map[string]string)

	if newEvent == false && strings.Index(rawMessage, OPENSNITCH_RULES_KEY) == -1 {
		return
	}
	if strings.Index(rawMessage, SYSCALL_SOCKET_STR) != -1 ||
		strings.Index(rawMessage, SYSCALL_CONNECT_STR) != -1 ||
		strings.Index(rawMessage, SYSCALL_SOCKETPAIR_STR) != -1 ||
		strings.Index(rawMessage, SYSCALL_EXECVE_STR) != -1 {

		extractFields(rawMessage, &aEvent)
		if aEvent != nil && isFromOurPid(aEvent["pid"], aEvent["ppid"]) {
			return
		}
		newEvent = true
		netEvent = &Event{}
		netEvent = populateEvent(netEvent, &aEvent)
		AddEvent(netEvent)

	} else if newEvent == true && strings.Index(rawMessage, AUDIT_TYPE_PROCTITLE) != -1 {
		extractFields(rawMessage, &aEvent)
		if aEvent == nil {
			return
		}
		netEvent = populateEvent(netEvent, &aEvent)
		AddEvent(netEvent)

	} else if newEvent == true && strings.Index(rawMessage, AUDIT_TYPE_CWD) != -1 {
		extractFields(rawMessage, &aEvent)
		if aEvent == nil {
			return
		}
		netEvent = populateEvent(netEvent, &aEvent)
		AddEvent(netEvent)

	} else if newEvent == true && strings.Index(rawMessage, AUDIT_TYPE_EXECVE) != -1 {
		extractFields(rawMessage, &aEvent)
		if aEvent == nil {
			return
		}
		netEvent = populateEvent(netEvent, &aEvent)
		AddEvent(netEvent)

	} else if newEvent == true && strings.Index(rawMessage, AUDIT_TYPE_PATH) != -1 {
		extractFields(rawMessage, &aEvent)
		if aEvent == nil {
			return
		}
		netEvent = populateEvent(netEvent, &aEvent)
		AddEvent(netEvent)

	} else if newEvent == true && strings.Index(rawMessage, AUDIT_TYPE_SOCKADDR) != -1 {
		extractFields(rawMessage, &aEvent)
		if aEvent == nil {
			return
		}
		if isFromOurPid(aEvent["pid"], aEvent["ppid"]) {
			return
		}

		netEvent = populateEvent(netEvent, &aEvent)
		AddEvent(netEvent)
		if EventChan != nil {
			eventChan <- *netEvent
		}

	} else if newEvent == true && strings.Index(rawMessage, AUDIT_TYPE_EOE) != -1 {
		newEvent = false
		if syscall.SYS_SOCKET == netEvent.Syscall && (netEvent.NetFamily == "" || netEvent.NetFamily[:4] != "inet") {
			log.Warning("Excluding event EOE", netEvent.NetFamily, netEvent)
			return
		}
		if netEvent.Pid == ourPid || netEvent.PPid == ourPid {
			return
		}

		AddEvent(netEvent)
		if EventChan != nil {
			eventChan <- *netEvent
		}
	}
}
