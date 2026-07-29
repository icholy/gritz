import {
	IDataObject,
	IExecuteFunctions,
	INodeExecutionData,
	NodeOperationError,
} from 'n8n-workflow';
import { createClient, ConnectError, type Client, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { toJson, type DescMessage, type MessageShape } from '@bufbuild/protobuf';
import {
	GritzService,
	TaskSchema,
	EventSchema,
	TaskLinkSchema,
	CancelTaskResponseSchema,
	ArchiveTaskResponseSchema,
	LifecycleKind,
	TaskStatus,
	type Event,
	type LifecyclePayload,
} from '../../gen/gritz/v1/gritz_pb';

export interface GritzApiCredentials {
	serverUrl: string;
	apiKey: string;
}

export function buildGritzClient(credentials: GritzApiCredentials): Client<typeof GritzService> {
	const authInterceptor: Interceptor = (next) => async (req) => {
		req.header.set('Authorization', `Bearer ${credentials.apiKey}`);
		return next(req);
	};
	const transport = createConnectTransport({
		baseUrl: credentials.serverUrl,
		interceptors: [authInterceptor],
	});
	return createClient(GritzService, transport);
}

const TERMINAL_STATUSES: TaskStatus[] = [
	TaskStatus.COMPLETED,
	TaskStatus.FAILED,
	TaskStatus.CANCELLED,
];

// renderLifecycle turns a lifecycle event into a readable activity line,
// mirroring the Go-side LifecyclePayload.Summary and the web UI renderer.
function renderLifecycle(p: LifecyclePayload): string {
	let s: string;
	switch (p.kind) {
		case LifecycleKind.CREATED:
			s = 'Created';
			break;
		case LifecycleKind.UPDATED:
			s = 'Updated';
			break;
		case LifecycleKind.CANCELLED:
			s = 'Cancelled';
			break;
		case LifecycleKind.RESTARTED:
			s = 'Restarted';
			break;
		case LifecycleKind.ARCHIVED:
			s = 'Archived';
			break;
		case LifecycleKind.UNARCHIVED:
			s = 'Unarchived';
			break;
		case LifecycleKind.AUTO_ARCHIVED:
			s = 'Auto-archived';
			break;
		case LifecycleKind.SANDBOX_STARTED:
			s = 'Sandbox started';
			break;
		case LifecycleKind.SANDBOX_EXITED:
			s = 'Sandbox exited';
			if (p.fromStatus && p.toStatus) s += ` (${p.fromStatus} -> ${p.toStatus})`;
			break;
		case LifecycleKind.SANDBOX_FAILED:
			s = 'Sandbox failed';
			if (p.message) s += `: ${p.message}`;
			break;
		default:
			s = 'Lifecycle event';
	}
	if (p.actor?.kind === 'user' && p.actor.name) s += ` by ${p.actor.name}`;
	return s;
}

export class GritzExecutor {
	private ctx: IExecuteFunctions;
	private client!: Client<typeof GritzService>;

	constructor(ctx: IExecuteFunctions) {
		this.ctx = ctx;
	}

	async execute(): Promise<INodeExecutionData[][]> {
		await this.buildClient();
		const items = this.ctx.getInputData();
		const returnData: INodeExecutionData[] = [];
		for (let i = 0; i < items.length; i++) {
			const operation = this.getStringParameter('operation', i);
			try {
				switch (operation) {
					case 'create':
						returnData.push(await this.create(i));
						break;
					case 'getDetails':
						returnData.push(await this.getDetails(i));
						break;
					case 'update':
						returnData.push(await this.update(i));
						break;
					case 'cancel':
						returnData.push(await this.cancel(i));
						break;
					case 'archive':
						returnData.push(await this.archive(i));
						break;
				}
			} catch (err) {
				if (err instanceof ConnectError) {
					throw new NodeOperationError(this.ctx.getNode(), err, {
						itemIndex: i,
					});
				}
				throw err;
			}
		}
		return [returnData];
	}

	private async buildClient(): Promise<void> {
		const credentials = await this.ctx.getCredentials<GritzApiCredentials>('GritzApi');
		this.client = buildGritzClient(credentials);
	}

	private getStringParameter(name: string, i: number): string {
		const v = this.ctx.getNodeParameter(name, i);
		if (typeof v !== 'string') {
			throw new NodeOperationError(
				this.ctx.getNode(),
				`Parameter "${name}" must be a string, got ${typeof v}`,
				{ itemIndex: i },
			);
		}
		return v;
	}

	private getNumberParameter(name: string, i: number): number {
		const v = this.ctx.getNodeParameter(name, i);
		if (typeof v !== 'number') {
			throw new NodeOperationError(
				this.ctx.getNode(),
				`Parameter "${name}" must be a number, got ${typeof v}`,
				{ itemIndex: i },
			);
		}
		return v;
	}

	private getBigIntParameter(name: string, i: number): bigint {
		const v = this.ctx.getNodeParameter(name, i);
		if (typeof v === 'bigint') return v;
		if (typeof v === 'number') return BigInt(v);
		if (typeof v === 'string') {
			try {
				return BigInt(v);
			} catch {
				throw new NodeOperationError(
					this.ctx.getNode(),
					`Parameter "${name}" is not a valid integer: "${v}"`,
					{ itemIndex: i },
				);
			}
		}
		throw new NodeOperationError(
			this.ctx.getNode(),
			`Parameter "${name}" must be a number or string, got ${typeof v}`,
			{ itemIndex: i },
		);
	}

	private getBooleanParameter(name: string, i: number): boolean {
		const v = this.ctx.getNodeParameter(name, i);
		if (typeof v !== 'boolean') {
			throw new NodeOperationError(
				this.ctx.getNode(),
				`Parameter "${name}" must be a boolean, got ${typeof v}`,
				{ itemIndex: i },
			);
		}
		return v;
	}

	private toJson<Desc extends DescMessage>(schema: Desc, msg: MessageShape<Desc>): IDataObject {
		return toJson(schema, msg) as IDataObject;
	}

	// activityLogs projects the report and lifecycle arms of a task's event
	// stream into flat {type, content} rows (the logs table is gone). The stream
	// is returned newest-first, so reverse it to render chronologically.
	private activityLogs(events: Event[]): IDataObject[] {
		const rows: IDataObject[] = [];
		for (const e of [...events].reverse()) {
			if (e.payload.case === 'report') {
				rows.push({ type: 'report', content: e.payload.value.content });
			} else if (e.payload.case === 'lifecycle') {
				rows.push({ type: 'lifecycle', content: renderLifecycle(e.payload.value) });
			}
		}
		return rows;
	}

	// taskDetails composes the {task, events, links, logs} bundle from the
	// primitive RPCs (getTask + listEventsByTask + listLinks) plus the projected
	// activity logs. It preserves the {task, events, links, logs} output shape
	// saved workflows key off, replacing the former server-side task-details
	// aggregator this node no longer calls.
	private async taskDetails(taskId: bigint): Promise<IDataObject> {
		const { task } = await this.client.getTask({ id: taskId });
		const { events } = await this.client.listEventsByTask({ taskId });
		const { links } = await this.client.listLinks({ taskId });
		return {
			task: this.toJson(TaskSchema, task!),
			events: events.map((e) => this.toJson(EventSchema, e)),
			links: links.map((l) => this.toJson(TaskLinkSchema, l)),
			logs: this.activityLogs(events),
		};
	}

	private async create(i: number): Promise<INodeExecutionData> {
		const resp = await this.client.createTask({
			runner: this.getStringParameter('runner', i),
			workspace: this.getStringParameter('workspace', i),
			instructions: [{
				text: this.getStringParameter('instruction', i)
			}],
			name: this.getStringParameter('taskName', i) || undefined,
		});

		const taskId = resp.task!.id;

		await this.wait(taskId, i);

		return {
			json: await this.taskDetails(taskId),
			pairedItem: { item: i },
		};
	}

	private async wait(taskId: bigint, i: number): Promise<void> {
		if (!this.getBooleanParameter('waitForCompletion', i)) {
			return;
		}
		const pollInterval = this.getNumberParameter('pollInterval', i);
		const timeout = this.getNumberParameter('timeout', i);
		const started = Date.now();
		while (true) {
			await new Promise((resolve) => setTimeout(resolve, pollInterval * 1000));

			if (timeout > 0 && Date.now() - started > timeout * 1000) {
				throw new NodeOperationError(
					this.ctx.getNode(),
					`Task ${taskId} did not complete within ${timeout}s`,
					{ itemIndex: i },
				);
			}

			const resp = await this.client.getTask({ id: taskId });
			const status = resp.task!.status;
			if (status === TaskStatus.FAILED) {
				throw new NodeOperationError(
					this.ctx.getNode(),
					`Task ${taskId} ended with status FAILED`,
					{ itemIndex: i },
				);
			}
			if (TERMINAL_STATUSES.includes(status)) {
				return;
			}
		}
	}

	private async getDetails(i: number): Promise<INodeExecutionData> {
		const taskId = this.getBigIntParameter('taskId', i);
		return {
			json: await this.taskDetails(taskId),
			pairedItem: { item: i },
		};
	}

	private async update(i: number): Promise<INodeExecutionData> {
		const taskId = this.getBigIntParameter('taskId', i);

		await this.client.updateTask({
			id: taskId,
			addInstructions: [{ text: this.getStringParameter('updateInstruction', i) }],
			start: this.getBooleanParameter('start', i),
		});

		await this.wait(taskId, i);

		return {
			json: await this.taskDetails(taskId),
			pairedItem: { item: i },
		};
	}

	private async cancel(i: number): Promise<INodeExecutionData> {
		const taskId = this.getBigIntParameter('taskId', i);
		const resp = await this.client.cancelTask({ id: taskId });
		return {
			json: this.toJson(CancelTaskResponseSchema, resp),
			pairedItem: { item: i },
		};
	}

	private async archive(i: number): Promise<INodeExecutionData> {
		const taskId = this.getBigIntParameter('taskId', i);

		await this.wait(taskId, i);

		const resp = await this.client.archiveTask({ id: taskId });
		return {
			json: this.toJson(ArchiveTaskResponseSchema, resp),
			pairedItem: { item: i },
		};
	}
}
