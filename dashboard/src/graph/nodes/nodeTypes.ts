import ArbitronNode from './ArbitronNode'
import LoopContainerNode from './LoopContainerNode'
import GroupContainerNode, { GroupFooterNode, GroupHeaderNode, GroupTaskCardNode } from './GroupContainerNode'

export const nodeTypes = {
  arbitronNode: ArbitronNode,
  loopContainer: LoopContainerNode,
  groupContainer: GroupContainerNode,
  groupHeader: GroupHeaderNode,
  groupFooter: GroupFooterNode,
  groupTaskCard: GroupTaskCardNode,
}
