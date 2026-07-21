import { useNavigate, useParams } from 'react-router-dom';
import { TaskModal } from '../components/TaskModal';

export function TaskPage() {
    const { taskId } = useParams<{ taskId: string }>();
    const navigate = useNavigate();

    return (
        <div className="h-full">
            <TaskModal
                taskId={taskId ? parseInt(taskId) : null}
                standalone
                onClose={() => navigate(-1)}
            />
        </div>
    );
}

export default TaskPage;
