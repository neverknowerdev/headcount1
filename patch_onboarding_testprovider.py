import re

with open("frontend/src/pages/Onboarding.tsx", "r") as f:
    content = f.read()

new_test_provider_ui = """    const [testLog, setTestLog] = useState<string | null>(null);
    const [showLog, setShowLog] = useState(false);

    useEffect(() => {
        if (name) {
            setCeoPrompt(`Your CEO of ${name}. Your goal is to keep an eye on tasks, delegate work to other agents, keep eye on their work, escalate to human if needed, and do whatever we need to achieve company goals`);
        }
    }, [name]);

    const handleCreateCompany = async (e: React.FormEvent) => {
        e.preventDefault();
        try {
            const res = await axios.post('/api/companies', { name, short_name: shortName, color });
            setCreatedCompanyId(res.data.id);
            setStep(2);
        } catch {
            alert('Failed to create company');
        }
    };

    const handleTestProvider = async () => {
        setIsTesting(true);
        setTestResult(null);
        setTestLog(null);
        setShowLog(false);
        try {
            const res = await axios.post('/api/providers/test', {
                base_url: providerUrl,
                api_key: providerKey,
                model: providerModel
            });
            setTestResult('success');
            if (res.data && res.data.log) {
                setTestLog(res.data.log);
            }
        } catch (error: any) {
            setTestResult('error');
            if (error.response && error.response.data && error.response.data.log) {
                setTestLog(error.response.data.log);
            } else if (error.message) {
                setTestLog(error.message);
            }
        } finally {
            setIsTesting(false);
        }
    };"""

content = re.sub(
    r'    useEffect\(\(\) => \{.*?finally \{\n            setIsTesting\(false\);\n        \}\n    \};',
    new_test_provider_ui,
    content,
    flags=re.DOTALL
)

new_form_ui = """                            <button type="button" onClick={handleTestProvider} disabled={isTesting} className="w-full bg-gray-100 text-gray-800 py-2 px-4 rounded-md border font-medium hover:bg-gray-200">
                                {isTesting ? 'Testing...' : 'Test Connection'}
                            </button>

                            {testResult === 'success' && <p className="text-green-600 text-sm font-semibold">Connection successful!</p>}
                            {testResult === 'error' && <p className="text-red-600 text-sm font-semibold">Connection failed. Check details and try again.</p>}

                            {testLog && (
                                <div className="mt-2 border rounded-md overflow-hidden">
                                    <button
                                        type="button"
                                        onClick={() => setShowLog(!showLog)}
                                        className="w-full text-left px-3 py-1.5 bg-gray-50 text-xs font-medium text-gray-600 hover:bg-gray-100 border-b flex justify-between"
                                    >
                                        {showLog ? 'Hide execution log' : 'Show execution log'}
                                        <span>{showLog ? '▲' : '▼'}</span>
                                    </button>
                                    {showLog && (
                                        <pre className="p-3 text-xs bg-gray-900 text-gray-300 overflow-x-auto whitespace-pre-wrap max-h-48">
                                            {testLog}
                                        </pre>
                                    )}
                                </div>
                            )}"""

content = re.sub(
    r'                            <button type="button" onClick=\{handleTestProvider\}.*?Connection failed\. Check details and try again\.</p>\}',
    new_form_ui,
    content,
    flags=re.DOTALL
)

with open("frontend/src/pages/Onboarding.tsx", "w") as f:
    f.write(content)
