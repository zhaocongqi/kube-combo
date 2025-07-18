# Debugger Pinger

## 1. UML

```mermaid
---
title: Debugger Pinger CRD
---
classDiagram
    note for Debugger "Debugger with(out) Pinger"
    Debugger <|-- Pinger
    Debugger <|-- ConfigMap
    note for Pinger "tcpping udpping ping nslookup tasks"
    Debugger <|-- Pod
    Debugger <|-- DaemonSet

    class Debugger {
        String WorkloadType
        String CPU
        String Memory
        String QoSBandwidth
        String Subnet
        Bool HostNetwork
        String Image
        Map Selector
        Map Tolerations
        Map Affinity
        String NodeName
        Bool EnablePinger
        String Pinger "pinger name"
        Bool EnableConfigMap
        String ConfigMap "cm name"
        Bool EnableSys "systemctl permissions"
        String RunAt "configmap string"

        Reconcile()
        GetDebugger()
        IsChange()
        GetPinger()
        HandlerAddOrUpdatePod()
        HandlerAddOrUpdateDaemonset()
        Update()
    }

   class ConfigMap {
        String Name
        String Namespace
        String Data "script content"
   }

   class Pinger {
        String Image
        Bool EnableMetrics
        String Ping
        String TcpPing
        String UdpPing
        String Dns

        Reconcile()
        GetPinger()
        IsChange()
        Update()
    }

   class Pod {
        Bool Hostnetwork
        String NodeName

        Create()
        Delete()
    }

   class DaemonSet {
     Bool Hostnetwork
     Map Selector
     Map Tolerations
     Map Affinity

     Create()
     Delete()
    }
```

Debugger CRD:

1. 控制 pod 的生命周期
2. 至少提供一个 pod 用于执行脚本：巡检，定位等

Pinger CRD：

1. 持久化维护 ping 测任务：`ping`  `udp`  `tcp`  `nslookup`
2. 可以选择是否启用 metrics

任务如果执行失败，会以 event 的形式记录到 pod

如果没有 Pinger，Debugger 只会启动一个容器

### 1.1 巡检任务执行方式(Pod 开始即运行｜exec 运行)

名字可以改

- 常规巡检：DS pod 常驻，基于 debugger spec runAt手动或者定时触发所有任务执行。
- （客户）自定义脚本巡检：Job pod 类型，用户可以通过 ConfigMap 注入脚本，脚本执行一次即完成。结果只能通过 logs 查看。
- （客户）自定义 pinger 巡检： 通过 pinger crd 维护 ping 测任务。异常结果只能通过 pod 状态和 logs 查看（log 足够可读性）。

### 1.2 一级概览任务下发，结果返回（展示）

- 任务下发基于 runAtOnce 的 configmap，configmap 的名字可以是一个时间，内容是巡检列表
- 返回通过 HTTP POST 

### 通过 HTTP POST 

各个 node 上 pod 检测完毕后， 直接 post  data 到前端，data 设计为 map：

node 为 key，value 为 map

**这个结构体前端定的，类似 alert 流程**

```bash

node1:
  disk:
    sdb1:
      err: "disk full" (错误码)
    sdb2:
  net:
    eth0:
    eth1:
    eth2:
      err: "link down" (错误码)
node2:
...

```



### 1.3 脚本目录设计

- `/scripts`：脚本目录

一级业务目录

- `/scripts/disk`：disk 检查相关脚本
- `/scripts/disk/errlog`：disk 检查相关 log: 按照 runAt 命名
- `/scripts/net`：net 检查相关脚本
- `/scripts/net/errlog`：net 检查相关 log: 按照 runAt 命名
- `/scripts/mem`mem 检查相关脚本
- `/scripts/mem/errlog`mem 检查相关 log: 按照 runAt 命名
- `/scripts/svc` svc 检查相关脚本
- `/scripts/svc/errlog` svc 检查相关 log: 按照 runAt 命名

每个一级业务目录下设计一个 errlog，只存储出现执行错误的命令和返回信息，便于精确定位。

正常执行的 log 不持久化存储，不需要考虑维护问题。

而且 pod 的 log 是可读的。

### 1.4 UI

- 1.基于各个 node 的 pod 分布式返回的结果展示一级概览
- 2.可以查看 pod log
- 3.可以 exec busybox pod 精确查看业务 errlog（每个业务只有一个 err log 文件）（已经有该功能）

### 1.5 权限与限制

- UI pod `exec --user` 需要使用限制性用户，所有文件只读，只能使用 cat ping 等基础命令。（命令参考 busybox 列表进行缩减）

### 1.6 pod 自清理机制

这种巡检 pod 本身并不重要，可以考虑每天凌晨重建一次，释放一下 log

## 2. Sequence

```mermaid
zenuml
    title Annotators
    @Control kubecombo
    @Database DebuggerCRD
    @Database PingerCRD
    @Database Pod

    par {
        kubecombo->DebuggerCRD: get the debugger?
        kubecombo->PingerCRD: get the pinger of debugger spec?
        kubecombo->Pod: exec into pod to run tasks list?
        kubecombo->Pod: curl to post result to UI？
        kubecombo->Pod: create | update | delete pod
    }
```

## 3. topo

ping gw

```mermaid
block-beta
columns 1
  block:ID
  A(("Pod1 on node1"))
  B(("Pod2 on node2"))
  C(("Pod3 on node3"))
  end
  space
  Switch
  A --"ping"--> Switch
  B --"ping"--> Switch
  C --"ping"--> Switch


```

默认统一 daemonset 内的 pinger 启动后，不同 node 上的 pod 都会进行互相 ping 测，到交换机网关则需要在 pinger spec 中指定网关 ip，而且 pod 本身启动时 kube-ovn-cni 会自动检测 ping 网关。
