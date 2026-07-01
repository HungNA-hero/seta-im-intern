const fs = require('fs');
const data = JSON.parse(fs.readFileSync('c:\\Users\\admin\\Downloads\\Seta Im Intern\\seta-im-intern\\.understand-anything\\tmp\\ua-arch-results.json', 'utf8'));

const layers = [
  {
    id: 'layer:access-core',
    name: 'Access Core Service',
    description: 'TypeScript and GraphQL microservice handling user identities, roles, and object permissions.',
    nodeIds: []
  },
  {
    id: 'layer:asset-core',
    name: 'Asset Core Service',
    description: 'Go microservice responsible for managing the folder tree hierarchy and asset metadata.',
    nodeIds: []
  },
  {
    id: 'layer:data',
    name: 'Data Layer',
    description: 'Database schemas, Flyway migrations, seed data, and Prisma models defining the persistence layer.',
    nodeIds: []
  },
  {
    id: 'layer:infrastructure',
    name: 'Infrastructure',
    description: 'Docker Compose definitions, database containers, and migration tools for local orchestration.',
    nodeIds: []
  },
  {
    id: 'layer:project',
    name: 'Project Documentation & Config',
    description: 'Root level documentation, project scripts, and developer tooling configurations.',
    nodeIds: []
  }
];

const mappedIds = new Set();

Object.values(data.directoryGroups).flat().forEach(nodeId => {
  if (mappedIds.has(nodeId)) return;

  const nodeType = nodeId.split(':')[0];

  if (nodeType === 'table' || nodeType === 'schema' || nodeId.includes('infra/db/')) {
    layers.find(l => l.id === 'layer:data').nodeIds.push(nodeId);
  } else if (nodeType === 'resource' || nodeType === 'service' || nodeId.includes('docker-compose')) {
    layers.find(l => l.id === 'layer:infrastructure').nodeIds.push(nodeId);
  } else if (nodeId.includes('access-core')) {
    layers.find(l => l.id === 'layer:access-core').nodeIds.push(nodeId);
  } else if (nodeId.includes('asset-core')) {
    layers.find(l => l.id === 'layer:asset-core').nodeIds.push(nodeId);
  } else {
    layers.find(l => l.id === 'layer:project').nodeIds.push(nodeId);
  }

  mappedIds.add(nodeId);
});

fs.mkdirSync('c:\\Users\\admin\\Downloads\\Seta Im Intern\\seta-im-intern\\.understand-anything\\intermediate', { recursive: true });
fs.writeFileSync('c:\\Users\\admin\\Downloads\\Seta Im Intern\\seta-im-intern\\.understand-anything\\intermediate\\layers.json', JSON.stringify(layers, null, 2));

const counts = layers.map(l => `${l.name}: ${l.nodeIds.length}`).join(', ');
console.log(`Successfully mapped ${mappedIds.size} nodes. Layers: ${counts}`);
