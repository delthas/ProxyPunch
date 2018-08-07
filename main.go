package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const relayHost = "delthas.fr:14761"

const defaultPort = 41254

var _, localIpv4, _ = net.ParseCIDR("127.0.0.0/8")
var _, localIpv6, _ = net.ParseCIDR("fc00::/7")

type Config struct {
	Mode       string `yaml:"mode"`
	LocalPort  int    `yaml:"local_port"`
	Host       string `yaml:"remote_host"`
	RemotePort int    `yaml:"remote_port"`
}

func client(host string, port int) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{
		Port: defaultPort,
	})
	if err != nil {
		c, err = net.ListenUDP("udp4", nil)
		if err != nil {
			log.Fatal(err)
		}
	}
	defer c.Close()

	localPort := c.LocalAddr().(*net.UDPAddr).Port
	fmt.Println("Listening, connect to 127.0.0.1:" + strconv.Itoa(localPort))

	relayAddr, err := net.ResolveUDPAddr("udp4", relayHost)
	if err != nil {
		log.Fatal(err)
	}

	remoteAddr, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		log.Fatal(err)
	}

	chRelay := make(chan struct{})
	go func() {
		relayPayload := append([]byte{byte(port >> 8), byte(port)}, remoteAddr.IP.To4()...)
		for {
			select {
			case <-chRelay:
				return
			default:
			}
			c.WriteToUDP(relayPayload, relayAddr)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	defer close(chRelay)

	buffer := make([]byte, 4096)

	for {
		n, addr, err := c.ReadFromUDP(buffer)
		if err != nil {
			// err is thrown if the buffer is too small
			continue
		}
		if !addr.IP.Equal(relayAddr.IP) || addr.Port != relayAddr.Port {
			continue
		}
		if n != 2 {
			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from relay. (size:"+strconv.Itoa(n)+")")
			continue
		}
		remoteAddr.Port = int(binary.BigEndian.Uint16(buffer[:2]))
		break
	}

	chPunch := make(chan struct{})
	go func() {
		punchPayload := []byte{0xCD}
		for {
			select {
			case <-chPunch:
				return
			default:
			}
			c.WriteToUDP(punchPayload, remoteAddr)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	defer close(chPunch)

	var localAddr net.UDPAddr
	for {
		n, addr, err := c.ReadFromUDP(buffer[1:])
		if err != nil {
			// err is thrown if the buffer is too small
			continue
		}
		if n > len(buffer)-1 {
			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from peer. (size:"+strconv.Itoa(n)+")")
			continue
		}
		if addr.IP.Equal(relayAddr.IP) && addr.Port == relayAddr.Port {
			continue
		}
		if addr.IP.Equal(remoteAddr.IP) && addr.Port == remoteAddr.Port {
			if n != 0 && localAddr.Port != 0 && buffer[1] == 0xCC {
				c.WriteToUDP(buffer[2:n+1], &localAddr)
			}
		} else if localIpv4.Contains(addr.IP) || localIpv6.Contains(addr.IP) {
			localAddr = *addr
			buffer[0] = 0xCC
			c.WriteToUDP(buffer[:n+1], remoteAddr)
		}
	}
}

func server(port int) {
	c, err := net.ListenUDP("udp4", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	fmt.Println("Listening, host at " + strconv.Itoa(port))

	localAddr := &net.UDPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: port,
	}

	relayAddr, err := net.ResolveUDPAddr("udp4", relayHost)
	if err != nil {
		log.Fatal(err)
	}

	chRelay := make(chan struct{})
	go func() {
		relayPayload := []byte{byte(port >> 8), byte(port)}
		for {
			select {
			case <-chRelay:
				return
			default:
			}
			c.WriteToUDP(relayPayload, relayAddr)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	defer close(chRelay)

	var remoteAddr net.UDPAddr
	buffer := make([]byte, 4096)

	for {
		n, addr, err := c.ReadFromUDP(buffer)
		if err != nil {
			// err is thrown if the buffer is too small
			continue
		}
		if !addr.IP.Equal(relayAddr.IP) || addr.Port != relayAddr.Port {
			continue
		}
		if n != 6 {
			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from relay. (size:"+strconv.Itoa(n)+")")
			continue
		}
		ip := make([]byte, 4)
		copy(ip, buffer[2:6])
		remoteAddr = net.UDPAddr{
			IP:   net.IP(ip),
			Port: int(binary.BigEndian.Uint16(buffer[:2])),
		}
		break
	}

	chPunch := make(chan struct{})
	go func() {
		punchPayload := []byte{0xCD}
		for {
			select {
			case <-chPunch:
				return
			default:
			}
			c.WriteToUDP(punchPayload, &remoteAddr)
			time.Sleep(500 * time.Millisecond)
		}
	}()
	defer close(chPunch)

	for {
		n, addr, err := c.ReadFromUDP(buffer[1:])
		if err != nil {
			// err is thrown if the buffer is too small
			continue
		}
		if n > len(buffer)-1 {
			fmt.Fprintln(os.Stderr, "Error received packet of wrong size from peer. (size:"+strconv.Itoa(n)+")")
			continue
		}
		if addr.IP.Equal(relayAddr.IP) && addr.Port == relayAddr.Port {
			continue
		}
		if addr.IP.Equal(remoteAddr.IP) && addr.Port == remoteAddr.Port {
			if n != 0 && buffer[1] == 0xCC {
				c.WriteToUDP(buffer[2:n+1], localAddr)
			}
		} else if (localIpv4.Contains(addr.IP) || localIpv6.Contains(addr.IP)) && addr.Port == port {
			buffer[0] = 0xCC
			c.WriteToUDP(buffer[:n+1], &remoteAddr)
		}
	}
}

func update(scanner *bufio.Scanner) bool {
	httpClient := http.Client{Timeout: 2 * time.Second}
	r, err := httpClient.Get("https://api.github.com/repos/delthas/proxypunch/releases")
	if err != nil {
		// throw error even if the user is just disconnected from the internet
		fmt.Fprintln(os.Stderr, "Error while looking for updates: "+err.Error())
		return false
	}
	var releases []struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name"`
		Assets  []struct {
			Name        string `json:"name"`
			DownloadUrl string `json:"browser_download_url"`
		} `json:"assets"`
	}
	decoder := json.NewDecoder(r.Body)
	err = decoder.Decode(&releases)
	r.Body.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error while processing updates list: "+err.Error())
		return false
	}
	for _, v := range releases {
		if v.TagName == ProgramVersion {
			return false
		}
		for _, asset := range v.Assets {
			if strings.Contains(asset.Name, ProgramArch) {
				update := ""
				for update != "y" && update != "yes" && update != "n" && update != "no" {
					fmt.Println("proxypunch update " + v.Name + " is available! Download and update now? y(es) / n(o) [yes]")
					if !scanner.Scan() {
						return false
					}
					update = strings.ToLower(scanner.Text())
					if update == "" {
						update = "y"
					}
				}
				if update != "y" && update != "yes" {
					return false
				}
				r, err = httpClient.Get(asset.DownloadUrl)
				if err != nil {
					// throw error even if the user is just disconnected from the internet
					fmt.Fprintln(os.Stderr, "Error while downloading update (http get): "+err.Error())
					return false
				}
				f, err := ioutil.TempFile("", "")
				if err != nil {
					r.Body.Close()
					// throw error even if the user is just disconnected from the internet
					fmt.Fprintln(os.Stderr, "Error while downloading update (file open): "+err.Error())
					return false
				}
				_, err = io.Copy(f, r.Body)
				r.Body.Close()
				f.Close()
				if err != nil {
					// throw error even if the user is just disconnected from the internet
					fmt.Fprintln(os.Stderr, "Error while downloading update (io copy): "+err.Error())
					return false
				}

				exe, err := os.Executable()
				if err != nil {
					fmt.Fprintln(os.Stderr, "Error while downloading update (exe path get): "+err.Error())
					return false
				}
				exe, err = filepath.EvalSymlinks(exe)
				if err != nil {
					fmt.Fprintln(os.Stderr, "Error while downloading update (exe path eval): "+err.Error())
					return false
				}

				var perm os.FileMode
				if info, err := os.Stat(exe); err != nil {
					perm = info.Mode()
				} else {
					perm = 0777
				}

				if runtime.GOOS == "windows" {
					err = os.Rename(exe, "proxypunch_old.exe")
					if err != nil {
						fmt.Fprintln(os.Stderr, "Error while downloading update (move current file): "+err.Error())
						return false
					}
				} else {
					err = os.Remove(exe)
					if err != nil {
						fmt.Fprintln(os.Stderr, "Error while downloading update (unlink current file): "+err.Error())
						return false
					}
				}

				w, err := os.OpenFile(exe, os.O_RDWR|os.O_CREATE|os.O_TRUNC, perm)
				if err != nil {
					fmt.Fprintln(os.Stderr, "Error while downloading update (create new file): "+err.Error())
					return false
				}

				r, err := os.Open(f.Name())
				if err != nil {
					w.Close()
					fmt.Fprintln(os.Stderr, "Error while downloading update (open update file): "+err.Error())
					return false
				}

				_, err = io.Copy(w, r)
				r.Close()
				w.Close()
				if err != nil {
					fmt.Fprintln(os.Stderr, "Error while downloading update (copy update file): "+err.Error())
					return false
				}

				cmd := exec.Command(exe, os.Args[1:]...)
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				cmd.Run()
				return true
			}
		}
	}
	return false
}

var ProgramVersion string
var ProgramArch string

func main() {
	if ProgramVersion == "" {
		ProgramVersion = "[Custom Build]"
	}
	fmt.Println("ProxyPunch " + ProgramVersion + " by delthas")
	fmt.Println()

	if runtime.GOOS == "windows" {
		// cleanup old update file, ignore error
		os.Remove("proxypunch_old.exe")
	}

	var mode string
	var host string
	var port int
	var noSave bool
	var noUpdate bool
	var configFile string

	flag.StringVar(&mode, "mode", "", "connect mode: server, client")
	flag.StringVar(&host, "host", "", "remote host for client mode: ipv4 or ipv6 or hostname")
	flag.IntVar(&port, "port", 0, "port for client or server mode")
	flag.BoolVar(&noSave, "nosave", false, "disable saving configuration to file")
	flag.BoolVar(&noUpdate, "noupdate", false, "disable automatic update")
	flag.StringVar(&configFile, "config", "proxypunch.yml", "load configuration from file")
	flag.Parse()

	scanner := bufio.NewScanner(os.Stdin)

	if !noUpdate && ProgramArch != "" && ProgramVersion != "[Custom Build]" {
		if update(scanner) {
			return
		}
	}

	var config Config

	noConfig := (mode == "server" && port != 0) || (mode == "client" && host != "" && port != 0)
	if !noConfig {
		file, err := os.Open(configFile)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintln(os.Stderr, "Error opening file "+configFile+": "+err.Error())
			}
		} else {
			decoder := yaml.NewDecoder(file)
			err = decoder.Decode(&config)
			file.Close()
			if err != nil {
				fmt.Fprintln(os.Stderr, "Error decoding config file "+configFile+". ("+err.Error()+")")
			}
			if config.Mode != "server" && config.Mode != "client" {
				config.Mode = ""
			}
			if config.LocalPort <= 0 || config.LocalPort > 65535 {
				config.LocalPort = 0
			}
			if config.RemotePort <= 0 || config.RemotePort > 65535 {
				config.RemotePort = 0
			}
		}
	}

	saveMode := mode == ""
	saveHost := host == ""
	savePort := port == 0

	for mode != "s" && mode != "server" && mode != "c" && mode != "client" {
		if config.Mode != "" {
			fmt.Println("Mode? s(erver) / c(lient) [" + config.Mode + "]")
		} else {
			fmt.Println("Mode? s(erver) / c(lient) ")
		}
		if !scanner.Scan() {
			return
		}
		mode = strings.ToLower(scanner.Text())
		if mode == "" {
			mode = config.Mode
		}
	}
	if saveMode {
		if mode == "s" {
			mode = "server"
		} else if mode == "c" {
			mode = "client"
		}
		config.Mode = mode
	}

	if mode == "c" || mode == "client" {
		for host == "" {
			if config.Host != "" {
				fmt.Println("Host? [" + config.Host + "]")
			} else {
				fmt.Println("Host? ")
			}
			if !scanner.Scan() {
				return
			}
			h := strings.ToLower(scanner.Text())
			if h == "" {
				host = config.Host
				continue
			}
			i := strings.IndexByte(h, ':')
			if i != -1 {
				var err error
				port, err = strconv.Atoi(h[i+1:])
				if err != nil {
					fmt.Println("Invalid host format, must be <host> or <host>:<port>")
					continue
				}
			} else {
				i = len(h)
			}
			host = h[:i]
		}
		if saveHost {
			config.Host = host
		}
	}

	var configPort int
	if mode == "c" || mode == "client" {
		configPort = config.RemotePort
	} else {
		configPort = config.LocalPort
	}
	for port == 0 {
		if configPort != 0 {
			fmt.Println("Port? [" + strconv.Itoa(configPort) + "]")
		} else {
			fmt.Println("Port? ")
		}
		if !scanner.Scan() {
			return
		}
		p := scanner.Text()
		if p == "" {
			port = configPort
			continue
		}
		port, _ = strconv.Atoi(scanner.Text())
	}
	if savePort {
		if mode == "c" || mode == "client" {
			config.RemotePort = port
		} else {
			config.LocalPort = port
		}
	}

	if !noConfig && !noSave && (saveHost || saveMode || savePort) {
		file, err := os.Create(configFile)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintln(os.Stderr, "Error opening file "+configFile+": "+err.Error())
			}
		} else {
			encoder := yaml.NewEncoder(file)
			err = encoder.Encode(&config)
			file.Close()
			if err != nil {
				fmt.Fprintln(os.Stderr, "Error saving config to file "+configFile+". ("+err.Error()+")")
			}
		}
	}

	if mode == "c" || mode == "client" {
		client(host, port)
	} else {
		server(port)
	}
}
