import {test} from 'node:test'
import assert from 'node:assert/strict'
import {supportedColumns} from './legacy-columns.mjs'
test('accept only the observed additive production recovery field',()=>{
 const table={name:'xgift_recovery_attempts',columns:[{name:'id',type:'TEXT',pk:1}]}
 assert(supportedColumns(table,table.columns))
 assert(supportedColumns(table,[{name:'previous_stage',type:'TEXT',pk:0},...table.columns]))
 assert(!supportedColumns(table,[...table.columns,{name:'unknown',type:'TEXT',pk:0}]))
 assert(!supportedColumns(table,[...table.columns,{name:'previous_stage',type:'INTEGER',pk:0}]))
 assert(!supportedColumns(table,[{name:'previous_stage',type:'TEXT',pk:0}]))
})
