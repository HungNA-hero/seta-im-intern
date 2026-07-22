import { GraphQLResolveInfo, SelectionSetNode } from "graphql";

export function selectionIncludesField(
  info: GraphQLResolveInfo,
  fieldName: string,
): boolean {
  const visitedFragments = new Set<string>();

  function includes(selectionSet: SelectionSetNode | undefined): boolean {
    if (!selectionSet) return false;
    for (const selection of selectionSet.selections) {
      if (selection.kind === "Field") {
        if (selection.name.value === fieldName) return true;
        if (includes(selection.selectionSet)) return true;
      } else if (selection.kind === "InlineFragment") {
        if (includes(selection.selectionSet)) return true;
      } else if (!visitedFragments.has(selection.name.value)) {
        visitedFragments.add(selection.name.value);
        if (includes(info.fragments[selection.name.value]?.selectionSet)) {
          return true;
        }
      }
    }
    return false;
  }

  return info.fieldNodes.some((fieldNode) => includes(fieldNode.selectionSet));
}
