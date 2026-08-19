export type ProjectTreeSource = {
  id: number
  name: string
  path_with_namespace: string
}

export type ProjectTreeNode<T extends ProjectTreeSource = ProjectTreeSource> = {
  key: string
  label: string
  kind: 'group' | 'project'
  projectCount: number
  project?: T
  children?: ProjectTreeNode<T>[]
}

export const buildProjectTree = <T extends ProjectTreeSource>(items: T[]): ProjectTreeNode<T>[] => {
  const roots: ProjectTreeNode<T>[] = []
  const groups = new Map<string, ProjectTreeNode<T>>()
  for (const project of items) {
    const segments = (project.path_with_namespace || project.name || `project-${project.id}`).split('/').filter(Boolean)
    const namespaceSegments = segments.slice(0, -1)
    let children = roots
    let namespacePath = ''
    for (const segment of namespaceSegments) {
      namespacePath = namespacePath ? `${namespacePath}/${segment}` : segment
      const key = `group:${namespacePath}`
      let group = groups.get(key)
      if (!group) {
        group = { key, label: segment, kind: 'group', projectCount: 0, children: [] }
        groups.set(key, group)
        children.push(group)
      }
      group.projectCount += 1
      children = group.children!
    }
    children.push({
      key: `project:${project.id}`,
      label: project.name || segments[segments.length - 1] || `项目 ${project.id}`,
      kind: 'project',
      projectCount: 1,
      project,
    })
  }
  const sortNodes = (nodes: ProjectTreeNode<T>[]) => {
    nodes.sort((left, right) => left.kind === right.kind
      ? left.label.localeCompare(right.label, 'zh-CN')
      : left.kind === 'group' ? -1 : 1)
    nodes.forEach(node => node.children && sortNodes(node.children))
  }
  sortNodes(roots)
  return roots
}
