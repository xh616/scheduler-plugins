package podcapacityofnode

import (
	"context"
	"fmt"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/ktesting"
	"testing"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	clientsetfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	fakeframework "k8s.io/kubernetes/pkg/scheduler/framework/fake"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/defaultbinder"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/queuesort"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	st "k8s.io/kubernetes/pkg/scheduler/testing"
	testutil "sigs.k8s.io/scheduler-plugins/test/util"
)

func TestPodCapacityOfNode(t *testing.T) {
	tests := []struct {
		nodeInfos    []*framework.NodeInfo
		wantErr      string
		expectedList framework.NodeScoreList
		name         string
	}{
		{
			// 正在删除的pod、预分配到该节点但尚未运行的pod、正常运行的pod、总pod
			// node1：0、0、110、110   -->  0
			// node2：3、0、10、110   -->  97
			// node3：0、0、0、110  --> 110
			nodeInfos:    []*framework.NodeInfo{makeNodeInfo("node1", 0, 0, 110, 110), makeNodeInfo("node2", 3, 0, 10, 110), makeNodeInfo("node3", 0, 0, 0, 110)},
			expectedList: []framework.NodeScore{{Name: "node1", Score: framework.MinNodeScore}, {Name: "node2", Score: 88}, {Name: "node3", Score: framework.MaxNodeScore}},
			name:         "pod capacity of node score.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger, ctx := ktesting.NewTestContext(t)
			cs := clientsetfake.NewSimpleClientset()
			informerFactory := informers.NewSharedInformerFactory(cs, 0)
			registeredPlugins := []st.RegisterPluginFunc{
				st.RegisterBindPlugin(defaultbinder.Name, defaultbinder.New),
				st.RegisterQueueSortPlugin(queuesort.Name, queuesort.New),
				st.RegisterPluginAsExtensions(Name, New, "Score"),
			}
			fakeSharedLister := &fakeSharedLister{nodes: test.nodeInfos}

			// 初始化框架和插件
			fh, err := st.NewFramework(
				ctx,
				registeredPlugins,
				"default-scheduler",
				frameworkruntime.WithClientSet(cs),
				frameworkruntime.WithInformerFactory(informerFactory),
				frameworkruntime.WithSnapshotSharedLister(fakeSharedLister),
				frameworkruntime.WithPodNominator(testutil.NewPodNominator(nil)),
			)
			if err != nil {
				t.Fatalf("fail to create framework: %s", err)
			}
			// initialize nominated pod by adding nominated pods into nominatedPodMap
			for _, n := range test.nodeInfos {
				for _, pi := range n.Pods {
					if pi.Pod.Status.NominatedNodeName != "" {
						addNominatedPod(logger, pi, n.Node().Name, fh)
					}
				}
			}
			pe, _ := New(nil, fh)
			var gotList framework.NodeScoreList
			// 计算节点得分
			plugin := pe.(framework.ScorePlugin)
			for i, n := range test.nodeInfos {
				score, err := plugin.Score(context.Background(), nil, nil, n.Node().Name)
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				gotList = append(gotList, framework.NodeScore{Name: test.nodeInfos[i].Node().Name, Score: score})
			}

			// 归一化得分并验证
			status := plugin.ScoreExtensions().NormalizeScore(context.Background(), nil, nil, gotList)
			if !status.IsSuccess() {
				t.Errorf("unexpected error: %v", status)
			}

			for i := range gotList {
				if test.expectedList[i].Score != gotList[i].Score {
					t.Errorf("expected %#v, got %#v", test.expectedList[i].Score, gotList[i].Score)
				}
			}
		})
	}
}

func makeNodeInfo(node string, terminatingPodNumber, nominatedPodNumber, regularPodNumber, pods int) *framework.NodeInfo {
	ni := framework.NewNodeInfo()
	// 添加终止中的 Pod
	for i := 0; i < terminatingPodNumber; i++ {
		podInfo := &framework.PodInfo{
			Pod: makeTerminatingPod(fmt.Sprintf("tpod_%s_%v", node, i+1)),
		}
		ni.Pods = append(ni.Pods, podInfo)
	}
	// 添加提名的 Pod
	for i := 0; i < nominatedPodNumber; i++ {
		podInfo := &framework.PodInfo{
			Pod: makeNominatedPod(fmt.Sprintf("npod_%s_%v", node, i+1), node),
		}
		ni.Pods = append(ni.Pods, podInfo)
	}
	// 添加普通 Pod
	for i := 0; i < regularPodNumber; i++ {
		podInfo := &framework.PodInfo{
			Pod: makeRegularPod(fmt.Sprintf("rpod_%s_%v", node, i+1)),
		}
		ni.Pods = append(ni.Pods, podInfo)
	}
	// 设置节点名称和容量
	ni.SetNode(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: node},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourcePods: *resource.NewQuantity(int64(pods), resource.DecimalExponent),
			},
		},
	})
	return ni
}

func makeTerminatingPod(name string) *v1.Pod {
	deletionTimestamp := metav1.Time{Time: time.Now()}
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			DeletionTimestamp: &deletionTimestamp,
		},
	}
}

func makeNominatedPod(podName string, nodeName string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			UID:  types.UID(podName),
		},
		Status: v1.PodStatus{
			NominatedNodeName: nodeName,
		},
	}
}

func makeRegularPod(name string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func addNominatedPod(logger klog.Logger, pi *framework.PodInfo, nodeName string, fh framework.Handle) *framework.PodInfo {
	fh.AddNominatedPod(logger, pi, &framework.NominatingInfo{NominatingMode: framework.ModeOverride, NominatedNodeName: nodeName})
	return pi
}

var _ framework.SharedLister = &fakeSharedLister{}

type fakeSharedLister struct {
	nodes []*framework.NodeInfo
}

func (f *fakeSharedLister) StorageInfos() framework.StorageInfoLister {
	return nil
}

func (f *fakeSharedLister) NodeInfos() framework.NodeInfoLister {
	return fakeframework.NodeInfoLister(f.nodes)
}
