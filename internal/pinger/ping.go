package pinger

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	goping "github.com/prometheus-community/pro-bing"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

func StartPinger(config *Configuration, stopCh <-chan struct{}) {
	errHappens := false
	withMetrics := config.Mode == "server" && config.EnableMetrics
	interval := time.Duration(config.Interval) * time.Second
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		if err := check(config, withMetrics); err != nil {
			klog.Errorf("check failed: %v", err)
			errHappens = true
		}
		if config.Mode != "server" {
			klog.V(3).Infof("pinger break, mode: %s", config.Mode)
			break
		}
		select {
		case <-stopCh:
			klog.V(3).Infof("pinger stopped")
			return
		case <-timer.C:
			klog.V(3).Infof("pinger check every %ds, errHappens: %v", config.Interval, errHappens)
			if errHappens && config.ExitCode != 0 {
				klog.Errorf("exit with code: %d", config.ExitCode)
				os.Exit(config.ExitCode)
			}
		}
		timer.Reset(interval)
	}
}

func check(config *Configuration, withMetrics bool) error {
	errHappens := false
	var err, happenErr error

	if config.Ping != "" {
		if err = pingExternal(config, withMetrics); err != nil {
			klog.Errorf("failed to ping: %v", err)
			errHappens = true
			happenErr = err
		}
	}
	if config.TCPPing != "" || config.UDPPing != "" {
		if err = checkAccessTargetIPPorts(config); err != nil {
			klog.Errorf("failed to check access to target ip ports: %v", err)
			errHappens = true
			happenErr = err
		}
	}
	if err = pingPods(config, withMetrics); err != nil {
		klog.Errorf("failed to ping pods: %v", err)
		errHappens = true
		happenErr = err
	}

	if config.EnableNodeIPCheck {
		if err = pingNodes(config, withMetrics); err != nil {
			klog.Errorf("failed to ping nodes : %v", err)
			errHappens = true
			happenErr = err
		}
	}

	if config.DnsLookup != "" {
		if err = dnslookup(config, withMetrics); err != nil {
			klog.Errorf("failed to dnslookup: %v", err)
			errHappens = true
			happenErr = err
		}
	}

	if errHappens {
		klog.Errorf("failed to check: %v, err: %v", errHappens, happenErr)
		return happenErr
	}
	return nil
}

func pingNodes(config *Configuration, setMetrics bool) error {
	klog.V(3).Infof("start to check node connectivity")
	nodes, err := config.KubeClient.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to list nodes, %v", err)
		return err
	}

	var pingErr error
	for _, no := range nodes.Items {
		for _, addr := range no.Status.Addresses {
			if addr.Type == v1.NodeInternalIP && slices.Contains(config.PodProtocols, CheckProtocol(addr.Address)) {
				func(nodeIP, nodeName string) {
					pinger, err := goping.NewPinger(nodeIP)
					if err != nil {
						pingErr = err
						klog.Errorf("failed to init pinger, %v", err)
						return
					}
					pinger.SetPrivileged(true)
					pinger.Timeout = 30 * time.Second
					pinger.Count = 3
					pinger.Interval = 100 * time.Millisecond
					pinger.Debug = true
					if err = pinger.Run(); err != nil {
						pingErr = err
						klog.Errorf("failed to run pinger for destination %s: %v", nodeIP, err)
						return
					}

					stats := pinger.Statistics()
					klog.Infof("ping node: %s %s, count: %d, loss count %d, average rtt %.2fms",
						nodeName, nodeIP, pinger.Count, int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))), float64(stats.AvgRtt)/float64(time.Millisecond))
					if int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))) != 0 {
						err := fmt.Errorf("ping node %s %s failed, packets sent: %d, packets received: %d",
							nodeName, nodeIP, stats.PacketsSent, stats.PacketsRecv)
						pingErr = err
						klog.Error(err)
						return
					}
					if setMetrics {
						SetNodePingMetrics(
							config.NodeName,
							config.HostIP,
							config.PodName,
							no.Name, addr.Address,
							float64(stats.AvgRtt)/float64(time.Millisecond),
							int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))),
							int(float64(stats.PacketsSent)))
					}
				}(addr.Address, no.Name)
			}
		}
	}
	return pingErr
}

func pingPods(config *Configuration, setMetrics bool) error {
	klog.V(3).Infof("start to check pod connectivity")
	if config.DaemonSetName == "" || config.DaemonSetNamespace == "" {
		klog.V(3).Infof("DaemonSetName %s or DaemonSetNamespace %s is empty, skip ping peer pods", config.DaemonSetName, config.DaemonSetNamespace)
		return nil
	}
	ds, err := config.KubeClient.AppsV1().DaemonSets(config.DaemonSetNamespace).Get(context.Background(), config.DaemonSetName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("failed to get peer ds: %v", err)
		return err
	}
	pods, err := config.KubeClient.CoreV1().Pods(config.DaemonSetNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: labels.Set(ds.Spec.Selector.MatchLabels).String()})
	if err != nil {
		klog.Errorf("failed to list peer pods: %v", err)
		return err
	}

	var pingErr error
	for _, pod := range pods.Items {
		for _, podIP := range pod.Status.PodIPs {
			if slices.Contains(config.PodProtocols, CheckProtocol(podIP.IP)) {
				func(podIP, podName, nodeIP, nodeName string) {
					pinger, err := goping.NewPinger(podIP)
					if err != nil {
						klog.Errorf("failed to init pinger, %v", err)
						pingErr = err
						return
					}
					pinger.SetPrivileged(true)
					pinger.Timeout = 1 * time.Second
					pinger.Debug = true
					pinger.Count = 3
					pinger.Interval = 100 * time.Millisecond
					if err = pinger.Run(); err != nil {
						klog.Errorf("failed to run pinger for destination %s: %v", podIP, err)
						pingErr = err
						return
					}

					stats := pinger.Statistics()
					klog.Infof("ping pod: %s %s, count: %d, loss count %d, average rtt %.2fms",
						podName, podIP, pinger.Count, int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))), float64(stats.AvgRtt)/float64(time.Millisecond))
					if int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))) != 0 {
						err := fmt.Errorf("ping pod %s %s failed, packets sent: %d, packets received: %d",
							podName, podIP, stats.PacketsSent, stats.PacketsRecv)
						pingErr = err
						klog.Error(err)
					}
					if setMetrics {
						SetPodPingMetrics(
							config.NodeName,
							config.HostIP,
							config.PodName,
							nodeName,
							nodeIP,
							podIP,
							float64(stats.AvgRtt)/float64(time.Millisecond),
							int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))),
							int(float64(stats.PacketsSent)))
					}
				}(podIP.IP, pod.Name, pod.Status.HostIP, pod.Spec.NodeName)
			}
		}
	}
	if pingErr != nil {
		klog.Errorf("failed to ping pods: %v", pingErr)
		return pingErr
	}
	return nil
}

func pingExternal(config *Configuration, setMetrics bool) error {
	if config.Ping == "" {
		return nil
	}
	var checkErr error
	addresses := strings.SplitSeq(config.Ping, ",")
	for addr := range addresses {
		if !slices.Contains(config.PodProtocols, CheckProtocol(addr)) {
			continue
		}

		klog.V(3).Infof("start to check ping %s", addr)
		pinger, err := goping.NewPinger(addr)
		if err != nil {
			klog.Errorf("failed to init pinger, %v", err)
			checkErr = err
			continue
		}
		pinger.SetPrivileged(true)
		pinger.Timeout = 5 * time.Second
		pinger.Debug = true
		pinger.Count = 3
		pinger.Interval = 100 * time.Millisecond
		if err = pinger.Run(); err != nil {
			klog.Errorf("failed to run pinger for destination %s: %v", addr, err)
			checkErr = err
			continue
		}
		stats := pinger.Statistics()
		klog.Infof("ping external address: %s, total count: %d, loss count %d, average rtt %.2fms",
			addr, pinger.Count, int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))), float64(stats.AvgRtt)/float64(time.Millisecond))
		if setMetrics {
			SetExternalPingMetrics(
				config.NodeName,
				config.HostIP,
				config.PodIP,
				addr,
				float64(stats.AvgRtt)/float64(time.Millisecond),
				int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))))
		}
		if int(math.Abs(float64(stats.PacketsSent-stats.PacketsRecv))) != 0 {
			err := fmt.Errorf("ping external address %s failed, packets sent: %d, packets received: %d",
				addr, stats.PacketsSent, stats.PacketsRecv)
			klog.Error(err)
			checkErr = err
			continue
		}
	}

	return checkErr
}

func checkAccessTargetIPPorts(config *Configuration) error {
	klog.V(3).Infof("start to check target ip ports connectivity")
	var checkErr error
	tcps := strings.SplitSeq(config.TCPPing, ",")
	for tcp := range tcps {
		if err := TCPConnectivityCheck(tcp); err != nil {
			klog.Errorf("TCP connectivity to %s failed", tcp)
			checkErr = err
		} else {
			klog.Infof("TCP connectivity to %s success", tcp)
		}
	}
	udps := strings.SplitSeq(config.UDPPing, ",")
	for udp := range udps {
		if err := UDPConnectivityCheck(udp); err != nil {
			klog.Errorf("UDP connectivity to %s failed", udp)
			checkErr = err
		} else {
			klog.Infof("UDP connectivity to %s success", udp)
		}
	}
	return checkErr
}

func dnslookup(config *Configuration, setMetrics bool) error {
	klog.V(3).Infof("start to dnslookup %s", config.DnsLookup)
	var checkErr error
	for dns := range strings.SplitSeq(config.DnsLookup, ",") {
		t1 := time.Now()
		ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
		defer cancel()
		var r net.Resolver
		addrs, err := r.LookupHost(ctx, dns)
		elapsed := time.Since(t1)
		if err != nil {
			klog.Errorf("failed to resolve dns %s, %v", dns, err)
			if setMetrics {
				SetDnsUnhealthyMetrics(config.NodeName)
			}
			checkErr = err
		}
		if setMetrics {
			SetDnsHealthyMetrics(config.NodeName, float64(elapsed)/float64(time.Millisecond))
		}
		klog.Infof("resolve %s to %v in %.2fms", dns, addrs, float64(elapsed)/float64(time.Millisecond))
	}
	return checkErr
}
