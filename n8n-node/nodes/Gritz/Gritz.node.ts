import {
	ICredentialsDecrypted,
	ICredentialTestFunctions,
	IExecuteFunctions,
	ILoadOptionsFunctions,
	INodeCredentialTestResult,
	INodeExecutionData,
	INodePropertyOptions,
	INodeType,
	INodeTypeDescription,
} from 'n8n-workflow';
import { GritzExecutor, buildGritzClient, type GritzApiCredentials } from './GritzExecutor';

export class Gritz implements INodeType {
	description: INodeTypeDescription = {
		displayName: 'Gritz',
		name: 'gritz',
		icon: 'file:gritz.png',
		group: ['transform'],
		version: 1,
		subtitle: '={{$parameter["operation"]}}',
		description: 'Create and run gritz tasks',
		defaults: { name: 'gritz' },
		inputs: ['main'],
		outputs: ['main'],
		credentials: [{ name: 'GritzApi', required: true, testedBy: 'ping' }],
		properties: [
			{
				displayName: 'Resource',
				name: 'resource',
				type: 'hidden',
				noDataExpression: true,
				default: 'task',
				options: [{ name: 'Task', value: 'task' }],
			},
			{
				displayName: 'Operation',
				name: 'operation',
				type: 'options',
				noDataExpression: true,
				displayOptions: { show: { resource: ['task'] } },
				options: [
					{
						name: 'Create',
						value: 'create',
						action: 'Create Task',
					},
					{
						name: 'Get Details',
						value: 'getDetails',
						action: 'Get Task Details',
					},
					{
						name: 'Update',
						value: 'update',
						action: 'Update Task',
					},
					{
						name: 'Cancel',
						value: 'cancel',
						action: 'Cancel Task',
					},
					{
						name: 'Archive',
						value: 'archive',
						action: 'Archive Task',
					},
				],
				default: 'create',
			},
			// Create fields
			{
				displayName: 'Runner',
				name: 'runner',
				type: 'options',
				typeOptions: { loadOptionsMethod: 'listRunners' },
				default: '',
				required: true,
				displayOptions: { show: { operation: ['create'] } },
				description: 'Runner ID that should handle this task',
			},
			{
				displayName: 'Workspace',
				name: 'workspace',
				type: 'options',
				typeOptions: {
					loadOptionsMethod: 'listWorkspaces',
					loadOptionsDependsOn: ['runner'],
				},
				default: '',
				required: true,
				displayOptions: { show: { operation: ['create'] } },
				description: 'Workspace to run the task in',
			},
			{
				displayName: 'Instruction',
				name: 'instruction',
				type: 'string',
				typeOptions: { rows: 4 },
				default: '',
				required: true,
				displayOptions: { show: { operation: ['create'] } },
				description: 'The instruction text for the task',
			},
			{
				displayName: 'Name',
				name: 'taskName',
				type: 'string',
				default: '',
				displayOptions: { show: { operation: ['create'] } },
				description: 'Optional name for the task',
			},
			// Task ID field (shared by getDetails, update, cancel, archive)
			{
				displayName: 'Task ID',
				name: 'taskId',
				type: 'number',
				default: 0,
				required: true,
				displayOptions: { show: { operation: ['getDetails', 'update', 'cancel', 'archive'] } },
				description: 'The task ID to operate on',
			},
			// Update operation fields
			{
				displayName: 'Instruction',
				name: 'updateInstruction',
				type: 'string',
				typeOptions: { rows: 4 },
				default: '',
				required: true,
				displayOptions: { show: { operation: ['update'] } },
				description: 'Instruction to add to the task',
			},
			{
				displayName: 'Start',
				name: 'start',
				type: 'boolean',
				default: true,
				displayOptions: { show: { operation: ['update'] } },
				description:
					'Whether to start the task after adding instructions (non-interrupting, waits for current run to finish)',
			},
			// Wait fields (shared by create, update, archive)
			{
				displayName: 'Wait for Completion',
				name: 'waitForCompletion',
				type: 'boolean',
				default: true,
				displayOptions: { show: { operation: ['create', 'update', 'archive'] } },
				description: 'Whether to poll the task until it reaches a terminal status before returning',
			},
			{
				displayName: 'Poll Interval (Seconds)',
				name: 'pollInterval',
				type: 'number',
				default: 10,
				displayOptions: {
					show: { operation: ['create', 'update', 'archive'], waitForCompletion: [true] },
				},
				description: 'How often to check task status',
			},
			{
				displayName: 'Timeout (Seconds)',
				name: 'timeout',
				type: 'number',
				default: 3600,
				displayOptions: {
					show: { operation: ['create', 'update', 'archive'], waitForCompletion: [true] },
				},
				description: 'Maximum time to wait before failing (0 = no timeout)',
			},
		],
	};

	methods = {
		credentialTest: {
			async ping(
				this: ICredentialTestFunctions,
				credential: ICredentialsDecrypted,
			): Promise<INodeCredentialTestResult> {
				try {
					const client = buildGritzClient(credential.data as unknown as GritzApiCredentials);
					await client.ping({});
					return { status: 'OK', message: 'Connected' };
				} catch (err) {
					if (err instanceof Error) {
						return { status: 'Error', message: err.message };
					}
					return { status: 'Error', message: 'Failed to connect to gritz server' };
				}
			},
		},
		loadOptions: {
			async listRunners(this: ILoadOptionsFunctions): Promise<INodePropertyOptions[]> {
				const credentials = await this.getCredentials<GritzApiCredentials>('GritzApi');
				const client = buildGritzClient(credentials);
				const resp = await client.listWorkspaces({});
				const runners = [...new Set(resp.workspaces.map((w) => w.runnerId))].sort();
				return runners.map((r) => ({ name: r, value: r }));
			},
			async listWorkspaces(this: ILoadOptionsFunctions): Promise<INodePropertyOptions[]> {
				const credentials = await this.getCredentials<GritzApiCredentials>('GritzApi');
				const runner = this.getCurrentNodeParameter('runner') as string;
				const client = buildGritzClient(credentials);
				const resp = await client.listWorkspaces({});
				return resp.workspaces
					.filter((w) => !runner || w.runnerId === runner)
					.map((w) => ({
						name: w.name,
						value: w.name,
						description: w.description,
					}));
			},
		},
	};

	async execute(this: IExecuteFunctions): Promise<INodeExecutionData[][]> {
		return new GritzExecutor(this).execute();
	}
}
