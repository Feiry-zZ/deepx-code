package agent

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// schedulerNode 是 DAG 中的一个执行单元。
// 一对一对应 PlanItem(tasks 子层已移除,plan = 节点)。
type schedulerNode struct {
	ID        string
	Title     string
	Model     string // "flash" | "pro" (PlanItem.Model 或 TaskItem.Model)
	DependsOn []string

	// 运行时
	Status  PlanStatus
	Summary string
}

// executeFunc 是节点的执行函数。scheduler 调用本函数跑实际工作 (子 agent / mock)。
//
// preds 是已完成的上游节点的总结,sub-agent 可以把它当作背景注入到自己的 system prompt。
// 返回的 summary 写到节点 .Summary,err != nil 时节点标 Failed。
type executeFunc func(n *schedulerNode, preds map[string]string) (summary string, err error)

// flattenPlans 把 []PlanItem 转成 scheduler 节点列表。
// 现在是 1:1 映射(plan = 节点);保留函数名是为了调用方不需要改。
func flattenPlans(plans []PlanItem) []schedulerNode {
	nodes := make([]schedulerNode, 0, len(plans))
	for _, p := range plans {
		nodes = append(nodes, schedulerNode{
			ID:        p.ID,
			Title:     p.Title,
			Model:     p.Model,
			DependsOn: append([]string(nil), p.DependsOn...),
		})
	}
	return nodes
}

// runDAG 按 DAG 依赖关系并发跑所有节点,直到全部进入终态 (Done/Failed/Blocked)。
// 入度为 0 的节点同时起跑;每完成一个就重新评估 ready 集合解锁下游。
// 上游 Failed/Blocked 时,下游标 Blocked 跳过 (失败传播)。
// 检测到循环依赖或孤儿依赖 → 把残留 pending 全标 Blocked 后退出。
//
// ctx 取消时,已启动的节点仍继续(子 agent 自己检测 ctx),等待中的节点标 Blocked。
// statusCh 非 nil 时,状态变化通过 TaskStatusMsg 同步给 UI。
// 返回的 nodes 含最终 Status 与 Summary。
func runDAG(ctx context.Context, nodes []schedulerNode, exec executeFunc, statusCh chan<- tea.Msg) []schedulerNode {
	if len(nodes) == 0 {
		return nodes
	}

	// 用 map 索引节点,方便按 id 找。注意 *schedulerNode 是切片内的指针,
	// 不能在 nodes 重新分配后访问。
	byID := make(map[string]*schedulerNode, len(nodes))
	for i := range nodes {
		nodes[i].Status = PlanStatusPending
		byID[nodes[i].ID] = &nodes[i]
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	done := make(chan struct{}, len(nodes))

	emit := func(id string, st PlanStatus, summary string) {
		if statusCh == nil {
			return
		}
		statusCh <- TaskStatusMsg{ID: id, Status: st, Summary: summary}
	}

	// 启动一个节点:状态置 running,在新 goroutine 里跑 exec,完成后写 Done/Failed,signal done。
	launch := func(n *schedulerNode, preds map[string]string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			summary, err := exec(n, preds)
			mu.Lock()
			if err != nil {
				n.Status = PlanStatusFailed
				n.Summary = "失败: " + err.Error()
			} else {
				n.Status = PlanStatusDone
				n.Summary = summary
			}
			finalSt, finalSum := n.Status, n.Summary
			mu.Unlock()
			emit(n.ID, finalSt, finalSum)
			done <- struct{}{}
		}()
	}

	// scheduler 主循环 (单线程驱动,只有它能往 launch goroutine 派工作)
	for {
		// context 取消时,全部 pending 标 Blocked 退出
		if ctx.Err() != nil {
			mu.Lock()
			for i := range nodes {
				n := &nodes[i]
				if n.Status == PlanStatusPending {
					n.Status = PlanStatusBlocked
					n.Summary = "已取消"
				}
			}
			mu.Unlock()
			break
		}

		// 一轮 ready/block 评估,在锁内完成所有状态变更与 preds 快照
		mu.Lock()
		var toLaunch []*schedulerNode
		var launchPreds []map[string]string
		var blocked []string
		anyRunning := false
		anyPending := false

		for i := range nodes {
			n := &nodes[i]
			switch n.Status {
			case PlanStatusRunning:
				anyRunning = true
				continue
			case PlanStatusDone, PlanStatusFailed, PlanStatusBlocked:
				continue
			}
			// pending: 检查依赖
			depFailed := false
			allDepsDone := true
			for _, dep := range n.DependsOn {
				dn, ok := byID[dep]
				if !ok {
					// 引用了不存在的节点 = 孤儿依赖,视为失败
					depFailed = true
					break
				}
				switch dn.Status {
				case PlanStatusFailed, PlanStatusBlocked:
					depFailed = true
				case PlanStatusDone:
					// ok
				default:
					allDepsDone = false
				}
				if depFailed {
					break
				}
			}
			if depFailed {
				n.Status = PlanStatusBlocked
				n.Summary = "上游失败,跳过"
				blocked = append(blocked, n.ID)
				continue
			}
			if !allDepsDone {
				// 真正还在 pending 的才计入 anyPending,
				// 本轮被转移到 Running/Blocked 的不算
				anyPending = true
				continue
			}
			// ready,占位 + 快照 preds
			n.Status = PlanStatusRunning
			preds := make(map[string]string, len(n.DependsOn))
			for _, dep := range n.DependsOn {
				if dn, ok := byID[dep]; ok {
					preds[dep] = dn.Summary
				}
			}
			toLaunch = append(toLaunch, n)
			launchPreds = append(launchPreds, preds)
		}
		mu.Unlock()

		// 锁外做副作用 (channel 发送 / 启动 goroutine)
		for _, id := range blocked {
			emit(id, PlanStatusBlocked, "上游失败,跳过")
		}
		for i, n := range toLaunch {
			emit(n.ID, PlanStatusRunning, "")
			launch(n, launchPreds[i])
		}

		// 退出条件:既无运行中也没启动新的。
		// 若 anyPending = true,说明剩下的全是循环/孤儿依赖,标 Blocked 后退出。
		if !anyRunning && len(toLaunch) == 0 {
			if anyPending {
				var deadlocked []string
				mu.Lock()
				for i := range nodes {
					n := &nodes[i]
					if n.Status == PlanStatusPending {
						n.Status = PlanStatusBlocked
						n.Summary = "循环依赖或孤儿依赖,无法调度"
						deadlocked = append(deadlocked, n.ID)
					}
				}
				mu.Unlock()
				for _, id := range deadlocked {
					emit(id, PlanStatusBlocked, "循环依赖或孤儿依赖,无法调度")
				}
			}
			break
		}

		// 至少有一个 launch 出去或还有 running, 等下一个完成再继续评估
		<-done
	}

	wg.Wait()
	return nodes
}
