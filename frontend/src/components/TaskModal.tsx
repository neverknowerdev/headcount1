/* eslint-disable @typescript-eslint/no-explicit-any */
import React, { useState, useEffect } from 'react';
import axios from 'axios';
import { X, Send } from 'lucide-react';

interface TaskModalProps {
    taskId: number;
    onClose: () => void;
}

export const TaskModal: React.FC<TaskModalProps> = ({ taskId, onClose }) => {
    const [task, setTask] = useState<any>(null);
    const [comments, setComments] = useState<any[]>([]);
    const [newComment, setNewComment] = useState('');

    useEffect(() => {
        const fetchDetails = async () => {
            try {
                const [taskRes, commentsRes] = await Promise.all([
                    axios.get(`/api/tasks/${taskId}`),
                    axios.get(`/api/comments?task_id=${taskId}`)
                ]);
                setTask(taskRes.data);
                setComments(commentsRes.data || []);
            } catch (e) {
                console.error(e);
            }
        };
        fetchDetails();

        const ws = new WebSocket(`ws://${window.location.host}/api/ws`);
        ws.onmessage = (event) => {
            const msg = JSON.parse(event.data);
            if (msg.type === 'comment_created' && msg.payload.task_id === taskId) {
                setComments(prev => [...prev, msg.payload]);
            }
        };
        return () => ws.close();
    }, [taskId]);

    const handleAddComment = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!newComment.trim()) return;
        try {
            await axios.post('/api/comments', {
                task_id: taskId,
                author_type: 'human',
                content: newComment
            });
            setNewComment('');
        } catch (e) {
            console.error(e);
        }
    };

    if (!task) return null;

    return (
        <div className="fixed inset-0 bg-black/50 bg-opacity-50 flex justify-center items-center z-50">
            <div className="bg-white rounded-lg shadow-xl w-full max-w-2xl max-h-[90vh] flex flex-col">
                <div className="p-6 border-b flex justify-between items-center">
                    <div>
                        <span className="text-xs font-semibold text-gray-500 uppercase tracking-wider block mb-1">Task T-{task.id}</span>
                        <h2 className="text-xl font-bold text-gray-900">{task.title}</h2>
                    </div>
                    <button onClick={onClose} className="text-gray-400 hover:text-gray-600"><X /></button>
                </div>

                <div className="flex-1 overflow-y-auto p-6 bg-gray-50 space-y-6">
                    {task.description && (
                        <div>
                            <h3 className="text-sm font-semibold text-gray-700 mb-2">Description</h3>
                            <p className="text-gray-600 text-sm whitespace-pre-wrap">{task.description}</p>
                        </div>
                    )}

                    <div>
                        <h3 className="text-sm font-semibold text-gray-700 mb-4 border-b pb-2">Comments & Activity</h3>
                        <div className="space-y-4" data-testid="comments-list">
                            {comments.length === 0 ? (
                                <p className="text-sm text-gray-500 italic">No comments yet.</p>
                            ) : (
                                comments.map((c: any) => (
                                    <div key={c.id} className={`flex flex-col ${c.author_type === 'agent' ? 'items-start' : 'items-end'}`}>
                                        <div className={`max-w-[80%] rounded-lg p-3 text-sm ${c.author_type === 'agent' ? 'bg-indigo-50 border border-indigo-100 text-gray-800' : 'bg-gray-200 text-gray-900'}`}>
                                            <span className="text-xs font-bold block mb-1 text-gray-500">
                                                {c.author_type === 'agent' ? '🤖 Agent' : '👤 You'}
                                            </span>
                                            <span className="whitespace-pre-wrap">{c.content}</span>
                                        </div>
                                    </div>
                                ))
                            )}
                        </div>
                    </div>
                </div>

                <div className="p-4 border-t bg-white">
                    <form onSubmit={handleAddComment} className="flex gap-2">
                        <input
                            type="text"
                            value={newComment}
                            onChange={(e) => setNewComment(e.target.value)}
                            placeholder="Add a comment..."
                            className="flex-1 border-gray-300 rounded-md shadow-sm border p-2 focus:ring-indigo-500 focus:border-indigo-500 text-sm"
                        />
                        <button type="submit" className="bg-indigo-600 text-white p-2 rounded-md hover:bg-indigo-700">
                            <Send size={18} />
                        </button>
                    </form>
                </div>
            </div>
        </div>
    );
};
