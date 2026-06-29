const { spawn } = require('child_process');
const path = require('path');
const os = require('os');

const pluginDashboardPath = path.join(os.homedir(), '.understand-anything', 'repo', 'understand-anything-plugin', 'packages', 'dashboard');

console.log(`Starting Understand Anything Dashboard...`);
console.log(`Graph Directory: ${process.cwd()}`);

const vite = spawn('npx', ['vite', '--host', '127.0.0.1'], {
    cwd: pluginDashboardPath,
    env: { ...process.env, GRAPH_DIR: process.cwd() },
    stdio: 'inherit',
    shell: true
});

vite.on('error', (err) => {
    console.error('Failed to start Vite:', err);
});
