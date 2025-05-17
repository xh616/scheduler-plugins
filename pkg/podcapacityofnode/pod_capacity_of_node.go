package podcapacityofnode

import (
	"context"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"math"
)

/*
优先调度到剩余pod容量最大的节点上
*/

type PodCapacityOfNode struct {
	handle framework.Handle
}

var _ = framework.ScorePlugin(&PodCapacityOfNode{})

// Name is the name of the plugin used in the Registry and configurations.
const Name = "PodCapacityOfNode"

func (ps *PodCapacityOfNode) Name() string { return Name }

func (ps *PodCapacityOfNode) Score(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	nodeInfo, err := ps.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return 0, framework.NewStatus(framework.Error, err.Error())
	}
	return ps.score(nodeInfo)
}

func (ps *PodCapacityOfNode) ScoreExtensions() framework.ScoreExtensions {
	return ps
}

func (ps *PodCapacityOfNode) score(nodeInfo *framework.NodeInfo) (int64, *framework.Status) {
	var (
		runningPodNum, nominatedPodNum, scoreOfCapacity, allPodNum int64
	)
	nodeName := nodeInfo.Node().Name
	// 正在运行的Pod数量
	runningPodNum = int64(len(nodeInfo.Pods))
	// 通过抢占机制提名到该节点的 Pod 数量
	nominatedPodNum = int64(len(ps.handle.NominatedPodsForNode(nodeName)))
	// 节点支持的最大 Pod 数量
	allPodNum = nodeInfo.Node().Status.Capacity.Pods().Value()
	// 打分，剩余pod数量越多分越高
	scoreOfCapacity = (allPodNum - (runningPodNum + nominatedPodNum)) * 100 / (allPodNum)

	klog.Infof("node %s score is %v\n", nodeName, scoreOfCapacity)

	return scoreOfCapacity, nil
}

func (ps *PodCapacityOfNode) NormalizeScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, scores framework.NodeScoreList) *framework.Status {
	// Find highest and lowest scores.
	var highest int64 = -math.MaxInt64
	var lowest int64 = math.MaxInt64
	for _, nodeScore := range scores {
		if nodeScore.Score > highest {
			highest = nodeScore.Score
		}
		if nodeScore.Score < lowest {
			lowest = nodeScore.Score
		}
	}
	// 将原始得分线性映射到 [MinNodeScore, MaxNodeScore]（即 0-100）
	oldRange := highest - lowest
	newRange := framework.MaxNodeScore - framework.MinNodeScore
	for i, nodeScore := range scores {
		if oldRange == 0 {
			scores[i].Score = framework.MinNodeScore
		} else {
			scores[i].Score = ((nodeScore.Score - lowest) * newRange / oldRange) + framework.MinNodeScore
		}
	}
	return nil
}

// New initializes a new plugin and returns it.
func New(_ runtime.Object, h framework.Handle) (framework.Plugin, error) {
	return &PodCapacityOfNode{handle: h}, nil
}
