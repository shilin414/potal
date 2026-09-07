"""Validation for portable agent workflow DAG definitions."""
from rest_framework.exceptions import ValidationError


def validate_workflow(value):
    if not value:
        return value
    if not isinstance(value, dict):
        raise ValidationError('workflow_config must be an object.')
    nodes = value.get('nodes', [])
    edges = value.get('edges', [])
    if not isinstance(nodes, list) or not isinstance(edges, list):
        raise ValidationError('Workflow nodes and edges must be lists.')
    ids = [str(node.get('id', '')) for node in nodes if isinstance(node, dict)]
    if not all(ids) or len(ids) != len(set(ids)):
        raise ValidationError('Every workflow node needs a unique id.')
    node_ids = set(ids)
    adjacency = {node_id: [] for node_id in node_ids}
    for edge in edges:
        source, target = str(edge.get('source', '')), str(edge.get('target', ''))
        if source not in node_ids or target not in node_ids:
            raise ValidationError('Workflow edge references an unknown node.')
        adjacency[source].append(target)
    visiting, visited = set(), set()

    def visit(node_id):
        if node_id in visiting:
            raise ValidationError('Workflow contains a cycle; use an explicit loop node.')
        if node_id in visited:
            return
        visiting.add(node_id)
        for target in adjacency[node_id]:
            visit(target)
        visiting.remove(node_id)
        visited.add(node_id)

    for node_id in node_ids:
        visit(node_id)
    return value
