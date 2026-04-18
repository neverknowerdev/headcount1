const fs = require('fs');

console.log('--- integration/llm_gateway.go ---');
console.log(fs.readFileSync('integration/llm_gateway.go', 'utf8').substring(6500, 7500));

console.log('--- server/controllers/providers.go ---');
console.log(fs.readFileSync('server/controllers/providers.go', 'utf8').substring(500, 1500));
